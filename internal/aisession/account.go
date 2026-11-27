package aisession

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
)

// ---- the account layer ("Phase 7 integration amendments": AGENTFLOW_HOME) ----
//
// A caller chooses AGENTFLOW_HOME, so a fresh one would reset the person's
// settings and every cap. When Options.AccountHome (the OS account's own
// agentflow home) differs from Options.Home, the person's restrictions
// there still hold: the account layer may only make a session stricter,
// never enable or loosen anything, and the caps count the sessions of both
// stores.

// accountLayer is the account home's configuration when it differs from
// the session home; nil when it is the same folder, unset or unreadable as
// absent.
func (o Options) accountLayer() (*config.User, string) {
	acct := o.AccountHome
	if acct == "" || !filepath.IsAbs(acct) || sameDir(acct, o.Home) {
		return nil, ""
	}
	if _, err := os.Stat(acct); err != nil {
		return nil, ""
	}
	u, err := config.LoadUserAt(acct)
	if err != nil {
		// A config the person wrote that does not load restricts nothing
		// it cannot name, but it must not be silently skipped either: treat
		// it as the strictest defaults agents may not change.
		off := false
		u = &config.User{}
		u.AI.Sessions.AgentStart = &off
	}
	return u, acct
}

// loadUser is the person's configuration for a session: the session
// home's, made stricter by the account layer. accountRules are the
// account layer's deny and ask rules, which the chain checks before any
// rule of the session home. With an account layer the session home keeps
// only its deny and ask rules too.
func (o Options) loadUser() (user *config.User, accountRules map[string]config.AIRule, err error) {
	user, err = config.LoadUserAt(o.Home)
	if err != nil {
		return nil, nil, err
	}
	acct, _ := o.accountLayer()
	if acct == nil {
		return user, nil, nil
	}
	out := strictest(user, acct)
	// A caller chose the session home, so its allow rules would loosen the
	// account's settings (a read confirm, say): with an account layer no
	// allow rule applies, from either layer. Deny and ask rules still do.
	out.AI.Permissions.Rules = restrictingRules(user.AI.Permissions.Rules)
	return out, restrictingRules(acct.AI.Permissions.Rules), nil
}

// restrictingRules keeps the deny and ask rules: the account layer's allow
// rules never apply on their own.
func restrictingRules(rules map[string]config.AIRule) map[string]config.AIRule {
	out := map[string]config.AIRule{}
	for name, r := range rules {
		if r.Decision == config.AIDecisionDeny || r.Decision == config.AIDecisionAsk {
			out[name] = r
		}
	}
	return out
}

func rank(l config.Level) int {
	switch l {
	case config.Auto:
		return 2
	case config.Confirm:
		return 1
	}
	return 0 // suggest
}

func stricterLevel(a, b config.Level) config.Level {
	if rank(b) < rank(a) {
		return b
	}
	return a
}

// classLevel is what a person's autonomy says for class (confirm unset).
func classLevel(a config.Autonomy, class string) config.Level {
	l := a.Local
	if class == "remote" {
		l = a.Remote
	}
	switch l {
	case config.Auto, config.Suggest, config.Confirm:
		return l
	}
	return config.Confirm
}

// strictest is u with every session-relevant setting at the stricter of u
// and acct.
func strictest(u, acct *config.User) *config.User {
	out := *u
	s, a := u.AI.Sessions, acct.AI.Sessions
	if !s.AgentStartOn() || !a.AgentStartOn() {
		off := false
		out.AI.Sessions.AgentStart = &off
	}
	if s.EffectiveHome() == config.AISessionHomeIsolated || a.EffectiveHome() == config.AISessionHomeIsolated {
		out.AI.Sessions.Home = config.AISessionHomeIsolated
	}
	if a.EffectiveUnasked() == config.AIUnaskedCancel {
		out.AI.Sessions.Unasked = config.AIUnaskedCancel
	}
	if !a.AgentflowMCPOn() {
		off := false
		out.AI.Sessions.AgentflowMCP = &off
	}
	n := min(s.EffectiveMaxRunning(), a.EffectiveMaxRunning())
	out.AI.Sessions.MaxRunning = &n
	for _, d := range []struct {
		dst      **time.Duration
		mine, ac time.Duration
	}{
		{&out.AI.Sessions.MaxDuration, s.EffectiveMaxDuration(), a.EffectiveMaxDuration()},
		{&out.AI.Sessions.IdleTimeout, s.EffectiveIdleTimeout(), a.EffectiveIdleTimeout()},
		{&out.AI.Sessions.AskTimeout, s.EffectiveAskTimeout(), a.EffectiveAskTimeout()},
	} {
		v := min(d.mine, d.ac)
		*d.dst = &v
	}
	out.AI.Harnesses.Disabled = slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(u.AI.Harnesses.Disabled), acct.AI.Harnesses.Disabled...))))
	// Autonomy: each class and each action at the lower level.
	auto := config.Autonomy{
		Local:  stricterLevel(classLevel(u.Autonomy, "local"), classLevel(acct.Autonomy, "local")),
		Remote: stricterLevel(classLevel(u.Autonomy, "remote"), classLevel(acct.Autonomy, "remote")),
	}
	actions := map[string]config.Level{}
	keys := slices.Collect(maps.Keys(u.Autonomy.Actions))
	keys = append(keys, slices.Collect(maps.Keys(acct.Autonomy.Actions))...)
	for _, action := range keys {
		k, ok := config.DescribeAutonomyAction(action)
		if !ok || len(k.Classes) == 0 {
			continue
		}
		class := k.Classes[0]
		level := func(a config.Autonomy) config.Level {
			if l, ok := a.Actions[action]; ok && l != "" {
				return l
			}
			if class == "read" {
				return config.Auto
			}
			return classLevel(a, class)
		}
		actions[action] = stricterLevel(level(u.Autonomy), level(acct.Autonomy))
	}
	if len(actions) > 0 {
		auto.Actions = actions
	}
	out.Autonomy = auto
	return &out
}

// accountStoreHome is the account's agentflow home when it is set and is
// not the session home: the store whose caps every session counts. Unlike
// accountLayer it does not need the folder to exist yet (the store creates
// it), so a fresh account still counts the sessions of every other home.
func (o Options) accountStoreHome() string {
	acct := o.AccountHome
	if acct == "" || !filepath.IsAbs(acct) || sameDir(acct, o.Home) {
		return ""
	}
	return acct
}

// accountStore opens the account store, or nil when there is none.
func (m *manager) accountStore() (*fileStore, error) {
	acct := m.o.accountStoreHome()
	if acct == "" {
		return nil, nil
	}
	return openStore(acct, m.o.Now)
}

// withAccountCounts runs create with the sessions of the account store
// that a start must count with its own: running ones, ones in the same
// worktree, and starts in the last hour (stubs included). It runs under the
// account store's lock, taken before the session store's, and writes the
// stub of the session create made into the account store before the lock
// is released, so a start from any other home counts it.
func (m *manager) withAccountCounts(worktree string, create func(running, here, recent int) (Session, error)) error {
	st, err := m.accountStore()
	if err != nil {
		// An account store that cannot be opened must not lift the caps.
		return refuse(RefuseLimitRunning, "The account's session store cannot be opened, so the session caps cannot be checked.", "Check the permissions of the account's agentflow home.")
	}
	if st == nil {
		_, err := create(0, 0, 0)
		return err
	}
	return st.locked(func() error {
		all, err := st.listLocked()
		if err != nil {
			// An account store that cannot be read must not lift the caps.
			return refuse(RefuseLimitRunning, "The account's session store cannot be read, so the session caps cannot be checked.", "Check the permissions of the agentflow home's ai folder.")
		}
		now := st.now()
		running, here, recent := 0, 0, 0
		for _, o := range all {
			if now.Sub(o.CreatedAt) < time.Hour {
				recent++
			}
			if o.State == StateEnded {
				continue
			}
			running++
			if sameDir(o.Worktree, worktree) {
				here++
			}
		}
		sess, err := create(running, here, recent)
		if err != nil {
			return err
		}
		return st.putStub(sess)
	})
}

// endStub marks the account store's stub of session id ended.
func (m *manager) endStub(id ID) {
	if st, err := m.accountStore(); err == nil && st != nil {
		st.endStub(id)
	}
}

package mcpserver

import "context"

type jiraRemoteLinkIn struct {
	Key          string `json:"key,omitempty" jsonschema:"Jira issue key, or use the current branch's issue"`
	URL          string `json:"url" jsonschema:"Absolute HTTP or HTTPS URL to add to Jira Web links"`
	Title        string `json:"title,omitempty" jsonschema:"Web link title, defaults to the URL"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"Single-use Devflow confirmation token"`
}

func (s *Server) registerJiraRemoteLink() {
	add(s, "devflow_jira_remote_link", "Add a Web link to an existing Jira issue through a connected Atlassian MCP or signed-in Teamwork Graph CLI.", remote,
		func(ctx context.Context, in jiraRemoteLinkIn) (any, error) {
			return s.app.LinkJiraRemote(ctx, in.Key, in.URL, in.Title, in.ConfirmToken)
		})
}

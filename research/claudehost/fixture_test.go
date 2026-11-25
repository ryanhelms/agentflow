// Package claudehost contains isolated protocol experiments for Devflow #68.
package claudehost

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const resourceURI = "ui://fixture/session.html"

type arguments struct {
	Text    string `json:"text"`
	IDs     []int  `json:"ids"`
	Enabled bool   `json:"enabled"`
}

type receipt struct {
	Connection string    `json:"connection"`
	Receipt    string    `json:"receipt"`
	Received   arguments `json:"received"`
}

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "fixture-provider" {
		if err := serveFixture(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func serveFixture(ledger string) error {
	file, err := os.OpenFile(ledger, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	connection := rand.Text()
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddResource(&mcp.Resource{Name: "probe", URI: resourceURI, MIMEType: "text/html;profile=mcp-app"},
		func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
				URI: resourceURI, MIMEType: "text/html;profile=mcp-app", Text: connection,
			}}}, nil
		})
	tool := &mcp.Tool{Name: "record", Description: "Record local fixture inputs",
		Meta: mcp.Meta{"ui": map[string]any{"resourceUri": resourceURI}}}
	mcp.AddTool(server, tool,
		func(_ context.Context, _ *mcp.CallToolRequest, input arguments) (*mcp.CallToolResult, receipt, error) {
			result := receipt{Connection: connection, Receipt: rand.Text(), Received: input}
			if err := json.NewEncoder(file).Encode(result); err != nil {
				return nil, receipt{}, err
			}
			// A cooperating provider can also speak the hook-output format.
			// The text stops a UserPromptSubmit probe before the model turn.
			hook, err := json.Marshal(map[string]string{"decision": "block", "reason": result.Receipt})
			if err != nil {
				return nil, receipt{}, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(hook)}}}, result, nil
		})
	return server.Run(context.Background(), &mcp.StdioTransport{})
}

func fixtureBinary(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOwnerCallsExactTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	ledger := filepath.Join(dir, "calls.jsonl")
	connect := func() *mcp.ClientSession {
		cmd := exec.CommandContext(ctx, fixtureBinary(t), "fixture-provider", ledger)
		cmd.Dir, cmd.Env = dir, []string{"HOME=" + dir}
		client := mcp.NewClient(&mcp.Implementation{Name: "fixture-owner", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		return session
	}
	call := func(session *mcp.ClientSession, input arguments) receipt {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "record", Arguments: input})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError || result.StructuredContent == nil {
			t.Fatalf("expected structured provider receipt, got %+v", result)
		}
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var got receipt
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Connection == "" || got.Receipt == "" || !reflect.DeepEqual(got.Received, input) {
			t.Fatalf("unexpected receipt: %+v", got)
		}
		return got
	}
	input := arguments{Text: "quoted \"input\"\n雪", IDs: []int{7, 0, 42}, Enabled: false}
	first := connect()
	a, b := call(first, input), call(first, input)
	c := call(connect(), input)
	if a.Connection != b.Connection || a.Connection == c.Connection || a.Receipt == b.Receipt {
		t.Fatal("expected two distinct receipts on one connection and a different second connection")
	}

	file, err := os.Open(ledger)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	dec := json.NewDecoder(file)
	for _, want := range []receipt{a, b, c} {
		var seen receipt
		if err := dec.Decode(&seen); err != nil || !reflect.DeepEqual(seen, want) {
			t.Fatalf("provider ledger disagrees with result: %+v, %v", seen, err)
		}
	}
	if err := dec.Decode(new(receipt)); err != io.EOF {
		t.Fatalf("expected end of provider ledger, got %v", err)
	}
}

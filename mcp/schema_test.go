package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/mcp"
)

// TestSchemaConversionNested covers schemaFromJSON's handling of nested
// object and array properties, required fields, enum, and default — the
// shapes a real MCP tool's inputSchema commonly uses.
func TestSchemaConversionNested(t *testing.T) {
	clientStream, serverStream := newPipedStreams()
	c := mcp.NewStdio(clientStream)
	t.Cleanup(func() { _ = c.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(serverStream)
		if err := handshake(r, serverStream); err != nil {
			return
		}
		req, err := readLine(r)
		if err != nil {
			return
		}
		result, _ := json.Marshal(map[string]any{
			"tools": []map[string]any{
				{
					"name":        "update",
					"description": "update a record",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"status": map[string]any{
								"type":        "string",
								"description": "new status",
								"enum":        []string{"open", "closed"},
								"default":     "open",
							},
							"tags": map[string]any{
								"type":  "array",
								"items": map[string]any{"type": "string"},
							},
							"owner": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"name": map[string]any{"type": "string"},
								},
							},
						},
						"required": []string{"status"},
					},
				},
			},
		})
		_ = writeLine(serverStream, wireMessage{JSONRPC: "2.0", ID: req.ID, Result: result})
	}()

	ctx := context.Background()
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := mcp.Project(ctx, c, "records")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	<-done
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	spec := tools[0].Spec()

	if spec.Type != "object" {
		t.Errorf("spec.Type = %q, want object", spec.Type)
	}
	if len(spec.Required) != 1 || spec.Required[0] != "status" {
		t.Errorf("spec.Required = %v, want [status]", spec.Required)
	}
	status, ok := spec.Properties["status"]
	if !ok {
		t.Fatal("spec.Properties[status] missing")
	}
	if status.Type != "string" || status.Description != "new status" || status.Default != "open" {
		t.Errorf("status = %+v, want type=string description=%q default=open", status, "new status")
	}
	if len(status.Enum) != 2 || status.Enum[0] != "open" || status.Enum[1] != "closed" {
		t.Errorf("status.Enum = %v, want [open closed]", status.Enum)
	}

	tags, ok := spec.Properties["tags"]
	if !ok {
		t.Fatal("spec.Properties[tags] missing")
	}
	if tags.Type != "array" || tags.Items == nil || tags.Items.Type != "string" {
		t.Errorf("tags = %+v, want array of string", tags)
	}

	owner, ok := spec.Properties["owner"]
	if !ok {
		t.Fatal("spec.Properties[owner] missing")
	}
	if owner.Type != "object" {
		t.Errorf("owner.Type = %q, want object", owner.Type)
	}
	name, ok := owner.Properties["name"]
	if !ok || name.Type != "string" {
		t.Errorf("owner.Properties[name] = %+v, ok=%v, want type=string", name, ok)
	}
}

// TestSchemaConversionEmptyDefaultsToObject covers the degrade-gracefully
// path: an absent or unparseable inputSchema must not fail the whole
// projection, since every builtin/registered tool needs SOME valid
// tool.Schema.
func TestSchemaConversionEmptyDefaultsToObject(t *testing.T) {
	clientStream, serverStream := newPipedStreams()
	c := mcp.NewStdio(clientStream)
	t.Cleanup(func() { _ = c.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(serverStream)
		if err := handshake(r, serverStream); err != nil {
			return
		}
		req, err := readLine(r)
		if err != nil {
			return
		}
		// No "inputSchema" field at all.
		result, _ := json.Marshal(map[string]any{
			"tools": []map[string]any{{"name": "noargs", "description": "takes nothing"}},
		})
		_ = writeLine(serverStream, wireMessage{JSONRPC: "2.0", ID: req.ID, Result: result})
	}()

	ctx := context.Background()
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := mcp.Project(ctx, c, "srv")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	<-done
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	if spec := tools[0].Spec(); spec.Type != "object" {
		t.Errorf("Spec().Type = %q, want object for a missing inputSchema", spec.Type)
	}
}

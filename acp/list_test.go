package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
)

func TestListSessionsRequestMarshal(t *testing.T) {
	tests := []struct {
		name string
		req  acp.ListSessionsRequest
		want string
	}{
		{
			name: "no filter, no cursor",
			req:  acp.ListSessionsRequest{},
			want: `{}`,
		},
		{
			name: "cwd and cursor set",
			req:  acp.ListSessionsRequest{Cwd: "/work", Cursor: "page-2"},
			want: `{"cwd":"/work","cursor":"page-2"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, got, tc.want)
		})
	}
}

func TestListSessionsRequestUnmarshal(t *testing.T) {
	data := []byte(`{"cursor":"page-2"}`)
	var req acp.ListSessionsRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if req.Cwd != "" || req.Cursor != "page-2" {
		t.Errorf("req = %+v, want Cwd=\"\" Cursor=\"page-2\"", req)
	}
}

func TestSessionInfoMarshal(t *testing.T) {
	tests := []struct {
		name string
		info acp.SessionInfo
		want string
	}{
		{
			name: "required fields only",
			info: acp.SessionInfo{SessionID: "sess-1", Cwd: "/work"},
			want: `{"sessionId":"sess-1","cwd":"/work"}`,
		},
		{
			name: "optional fields set",
			info: acp.SessionInfo{
				SessionID: "sess-1",
				Cwd:       "/work",
				Title:     "fix the bug",
				UpdatedAt: "2026-07-12T00:00:00Z",
			},
			want: `{"sessionId":"sess-1","cwd":"/work","title":"fix the bug","updatedAt":"2026-07-12T00:00:00Z"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.info)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, got, tc.want)
		})
	}
}

func TestListSessionsResponseMarshal(t *testing.T) {
	tests := []struct {
		name string
		resp acp.ListSessionsResponse
		want string
	}{
		{
			name: "empty sessions slice marshals as empty array",
			resp: acp.ListSessionsResponse{Sessions: []acp.SessionInfo{}},
			want: `{"sessions":[]}`,
		},
		{
			name: "sessions with next cursor",
			resp: acp.ListSessionsResponse{
				Sessions:   []acp.SessionInfo{{SessionID: "sess-1", Cwd: "/work"}},
				NextCursor: "page-2",
			},
			want: `{"sessions":[{"sessionId":"sess-1","cwd":"/work"}],"nextCursor":"page-2"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.resp)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, got, tc.want)
			// Assert the exact byte shape for the empty-slice case: []
			// (not null) since assertJSONEqual normalizes both to the same
			// decoded value and would not catch a "null" regression.
			if tc.name == "empty sessions slice marshals as empty array" && string(got) != tc.want {
				t.Errorf("Marshal() = %s, want exact %s", got, tc.want)
			}
		})
	}
}

func TestLoadSessionResponseMarshal(t *testing.T) {
	got, err := json.Marshal(acp.LoadSessionResponse{})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(got) != `{}` {
		t.Errorf("Marshal() = %s, want {}", got)
	}
}

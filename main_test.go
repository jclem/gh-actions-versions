package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
)

type mockResponse struct {
	body []byte
	err  error
}

type mockRESTClient struct {
	t          *testing.T
	responses  map[string]mockResponse
	callCounts map[string]int
}

func newMockRESTClient(t *testing.T) *mockRESTClient {
	t.Helper()
	return &mockRESTClient{
		t:          t,
		responses:  make(map[string]mockResponse),
		callCounts: make(map[string]int),
	}
}

func (m *mockRESTClient) withJSON(path string, payload interface{}) *mockRESTClient {
	m.t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		m.t.Fatalf("failed to marshal payload for %s: %v", path, err)
	}
	m.responses[path] = mockResponse{body: data}
	return m
}

func (m *mockRESTClient) withError(path string, err error) *mockRESTClient {
	m.t.Helper()
	m.responses[path] = mockResponse{err: err}
	return m
}

func (m *mockRESTClient) Get(path string, response interface{}) error {
	m.callCounts[path]++
	res, ok := m.responses[path]
	if !ok {
		m.t.Fatalf("unexpected GET %q", path)
	}
	if res.err != nil {
		return res.err
	}
	if len(res.body) == 0 {
		return nil
	}
	if err := json.Unmarshal(res.body, response); err != nil {
		m.t.Fatalf("failed to unmarshal response for %s: %v", path, err)
	}
	return nil
}

func TestClassifyVersionSpec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		spec       string
		wantKind   versionSpecKind
		wantNormal string
	}{
		{"v1.2.3", specExact, "v1.2.3"},
		{"1.2.3", specExact, "v1.2.3"},
		{"V1.2.3", specExact, "v1.2.3"},
		{"v1.2", specMinor, "v1.2"},
		{"1.2", specMinor, "v1.2"},
		{"v1", specMajor, "v1"},
		{"1", specMajor, "v1"},
		{"main", specUnknown, "main"},
	}
	for _, tc := range cases {
		kind, normalized := classifyVersionSpec(tc.spec)
		if kind != tc.wantKind {
			t.Fatalf("classifyVersionSpec(%q) kind = %v, want %v", tc.spec, kind, tc.wantKind)
		}
		if normalized != tc.wantNormal {
			t.Fatalf("classifyVersionSpec(%q) normalized = %q, want %q", tc.spec, normalized, tc.wantNormal)
		}
	}
}

func TestEnsureLeadingV(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"v1.2.3": "v1.2.3",
		"1.2.3":  "v1.2.3",
		"V2":     "v2",
		"alpha":  "alpha",
	}
	for input, want := range cases {
		if got := ensureLeadingV(input); got != want {
			t.Fatalf("ensureLeadingV(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMatchVersionSpec(t *testing.T) {
	t.Parallel()
	if !matchVersionSpec("v2.3.4", "v2", specMajor) {
		t.Fatal("expected v2.3.4 to match major spec v2")
	}
	if !matchVersionSpec("v2.3.4", "v2.3", specMinor) {
		t.Fatal("expected v2.3.4 to match minor spec v2.3")
	}
	if matchVersionSpec("v2.3.4", "v2.4", specMinor) {
		t.Fatal("expected v2.3.4 not to match minor spec v2.4")
	}
	if !matchVersionSpec("v2.3.4", "v2.3.4", specExact) {
		t.Fatal("expected v2.3.4 to match exact spec v2.3.4")
	}
	if matchVersionSpec("v1.2.3", "main", specUnknown) {
		t.Fatal("unknown spec should only match identical strings")
	}
	if !matchVersionSpec("main", "main", specUnknown) {
		t.Fatal("identical unknown specs should match")
	}
}

func TestSplitValueAndComment(t *testing.T) {
	t.Parallel()
	val, comment := splitValueAndComment(`actions/checkout@v3 # use latest v3`)
	if val != "actions/checkout@v3" || comment != "use latest v3" {
		t.Fatalf("splitValueAndComment simple case got (%q, %q)", val, comment)
	}

	val, comment = splitValueAndComment(`"owner/repo@v1#withhash" # keep hash`)
	if val != `"owner/repo@v1#withhash"` || comment != "keep hash" {
		t.Fatalf("splitValueAndComment quoted case got (%q, %q)", val, comment)
	}

	val, comment = splitValueAndComment(`'owner/repo@v1#tag'`)
	if val != `'owner/repo@v1#tag'` || comment != "" {
		t.Fatalf("splitValueAndComment single quoted case got (%q, %q)", val, comment)
	}
}

func TestSplitAndJoinComment(t *testing.T) {
	t.Parallel()
	version, suffix := splitComment("v1.2.3 alpha")
	if version != "v1.2.3" || suffix != "alpha" {
		t.Fatalf("splitComment unexpected (%q,%q)", version, suffix)
	}
	if _, suffix = splitComment(""); suffix != "" {
		t.Fatalf("splitComment empty suffix = %q", suffix)
	}
	if joined := joinComment("v1.2.3", "alpha"); joined != "v1.2.3 alpha" {
		t.Fatalf("joinComment got %q", joined)
	}
	if joined := joinComment("v1.2.3", ""); joined != "v1.2.3" {
		t.Fatalf("joinComment without suffix got %q", joined)
	}
	if joined := joinComment("", "alpha"); joined != "alpha" {
		t.Fatalf("joinComment without version got %q", joined)
	}
}

func TestParseUsesLine(t *testing.T) {
	t.Parallel()
	line := `  - uses: owner/repo/path@ref # note`
	usage, ok := parseUsesLine(line)
	if !ok {
		t.Fatal("expected parseUsesLine to succeed")
	}
	if usage.Spec.Owner != "owner" || usage.Spec.Repo != "repo" || usage.Spec.Path != "path" {
		t.Fatalf("unexpected spec: %+v", usage.Spec)
	}
	if usage.Ref != "ref" {
		t.Fatalf("unexpected ref %q", usage.Ref)
	}
	if usage.Comment != "note" {
		t.Fatalf("unexpected comment %q", usage.Comment)
	}
	if usage.Indent != "  - " {
		t.Fatalf("unexpected indent %q", usage.Indent)
	}
}

func TestTagResolverResolveSpecMajor(t *testing.T) {
	t.Parallel()
	mock := newMockRESTClient(t).
		withJSON("repos/owner/repo/releases?per_page=100&page=1", []map[string]interface{}{
			{"tag_name": "v2.3.4", "prerelease": false},
		}).
		withJSON("repos/owner/repo/git/ref/tags/v2.3.4", map[string]interface{}{
			"object": map[string]interface{}{
				"sha":  "ABCDEF1234567890ABCDEF1234567890ABCDEF12",
				"type": "commit",
			},
		})

	resolver := NewTagResolver(mock)
	tag, commit, err := resolver.ResolveSpec("owner", "repo", "v2")
	if err != nil {
		t.Fatalf("ResolveSpec error: %v", err)
	}
	if tag != "v2.3.4" {
		t.Fatalf("expected tag v2.3.4 got %s", tag)
	}
	if commit != strings.ToLower("ABCDEF1234567890ABCDEF1234567890ABCDEF12") {
		t.Fatalf("unexpected commit %s", commit)
	}
}

func TestTagResolverResolveAnnotatedTag(t *testing.T) {
	t.Parallel()
	mock := newMockRESTClient(t).
		withJSON("repos/o/r/releases?per_page=100&page=1", []map[string]interface{}{
			{"tag_name": "v1.0.0", "prerelease": false},
		}).
		withJSON("repos/o/r/git/ref/tags/v1.0.0", map[string]interface{}{
			"object": map[string]interface{}{
				"sha":  "annotated-sha",
				"type": "tag",
			},
		}).
		withJSON("repos/o/r/git/tags/annotated-sha", map[string]interface{}{
			"object": map[string]interface{}{
				"sha":  "1234567890abcdef1234567890abcdef12345678",
				"type": "commit",
			},
		})

	resolver := NewTagResolver(mock)
	tag, commit, err := resolver.ResolveSpec("o", "r", "v1")
	if err != nil {
		t.Fatalf("ResolveSpec error: %v", err)
	}
	if tag != "v1.0.0" {
		t.Fatalf("expected v1.0.0, got %s", tag)
	}
	if commit != "1234567890abcdef1234567890abcdef12345678" {
		t.Fatalf("unexpected commit %s", commit)
	}
}

func TestTagResolverResolveSpecFallbackToTags(t *testing.T) {
	t.Parallel()
	mock := newMockRESTClient(t).
		withError("repos/owner/repo/releases?per_page=100&page=1", &api.HTTPError{
			StatusCode: 404,
			RequestURL: &url.URL{Path: "releases"},
		}).
		withJSON("repos/owner/repo/tags?per_page=100&page=1", []map[string]interface{}{
			{"name": "v1.2.5"},
		}).
		withJSON("repos/owner/repo/git/ref/tags/v1.2.5", map[string]interface{}{
			"object": map[string]interface{}{
				"sha":  "ffffffffffffffffffffffffffffffffffffffff",
				"type": "commit",
			},
		})

	resolver := NewTagResolver(mock)
	tag, commit, err := resolver.ResolveSpec("owner", "repo", "v1.2")
	if err != nil {
		t.Fatalf("ResolveSpec error: %v", err)
	}
	if tag != "v1.2.5" {
		t.Fatalf("expected v1.2.5 got %s", tag)
	}
	if commit != "ffffffffffffffffffffffffffffffffffffffff" {
		t.Fatalf("unexpected commit %s", commit)
	}
}

func TestTagResolverResolveSpecExactFallback(t *testing.T) {
	t.Parallel()
	mock := newMockRESTClient(t).
		withError("repos/owner/repo/git/ref/tags/V1.2.3", &api.HTTPError{
			StatusCode: 404,
			RequestURL: &url.URL{Path: "git/ref"},
		}).
		withJSON("repos/owner/repo/git/ref/tags/v1.2.3", map[string]interface{}{
			"object": map[string]interface{}{
				"sha":  "ABCDEF1234567890ABCDEF1234567890ABCDEF12",
				"type": "commit",
			},
		})
	resolver := NewTagResolver(mock)
	tag, commit, err := resolver.ResolveSpec("owner", "repo", "V1.2.3")
	if err != nil {
		t.Fatalf("ResolveSpec error: %v", err)
	}
	if tag != "v1.2.3" {
		t.Fatalf("expected v1.2.3 got %s", tag)
	}
	if commit != strings.ToLower("ABCDEF1234567890ABCDEF1234567890ABCDEF12") {
		t.Fatalf("unexpected commit %s", commit)
	}
}

func TestTagResolverResolveSpecNoMatch(t *testing.T) {
	t.Parallel()
	mock := newMockRESTClient(t).
		withJSON("repos/owner/repo/releases?per_page=100&page=1", []map[string]interface{}{}).
		withJSON("repos/owner/repo/tags?per_page=100&page=1", []map[string]interface{}{})
	resolver := NewTagResolver(mock)
	if _, _, err := resolver.ResolveSpec("owner", "repo", "v9"); err == nil {
		t.Fatal("expected ResolveSpec to fail when no tags match")
	}
}

func TestRunUpdate(t *testing.T) {
	t.Parallel()
	const initialCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const resolvedCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	mock := newMockRESTClient(t).
		withJSON("repos/actions/checkout/releases?per_page=100&page=1", []map[string]interface{}{
			{"tag_name": "v5.0.0", "prerelease": false},
		}).
		withJSON("repos/actions/checkout/git/ref/tags/v5.0.0", map[string]interface{}{
			"object": map[string]interface{}{
				"sha":  resolvedCommit,
				"type": "commit",
			},
		})

	wf := buildWorkflowFile(t, `      - uses: actions/checkout@`+initialCommit+` # v5`)
	exit := runUpdate(mock, []*WorkflowFile{wf}, []string{"actions/checkout"})
	if exit != 0 {
		t.Fatalf("runUpdate exit = %d, want 0", exit)
	}
	if wf.Uses[0].Ref != resolvedCommit {
		t.Fatalf("usage ref = %s, want %s", wf.Uses[0].Ref, resolvedCommit)
	}
	expectedLine := `      - uses: actions/checkout@` + resolvedCommit + ` # v5`
	if wf.Lines[0] != expectedLine {
		t.Fatalf("updated line = %q, want %q", wf.Lines[0], expectedLine)
	}
}

func TestRunFix(t *testing.T) {
	t.Parallel()
	const wrongCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const resolvedCommit = "cccccccccccccccccccccccccccccccccccccccc"

	mock := newMockRESTClient(t).
		withJSON("repos/actions/checkout/releases?per_page=100&page=1", []map[string]interface{}{
			{"tag_name": "v5.0.0", "prerelease": false},
		}).
		withJSON("repos/actions/checkout/git/ref/tags/v5.0.0", map[string]interface{}{
			"object": map[string]interface{}{
				"sha":  resolvedCommit,
				"type": "commit",
			},
		})

	wf := buildWorkflowFile(t, `      - uses: actions/checkout@`+wrongCommit+` # v5.0.0`)
	exit := runFix(mock, []*WorkflowFile{wf})
	if exit != 0 {
		t.Fatalf("runFix exit = %d, want 0", exit)
	}
	expectedLine := `      - uses: actions/checkout@` + resolvedCommit + ` # v5.0.0`
	if wf.Lines[0] != expectedLine {
		t.Fatalf("updated line = %q, want %q", wf.Lines[0], expectedLine)
	}
}

func TestRunVerify(t *testing.T) {
	t.Parallel()
	const correctCommit = "dddddddddddddddddddddddddddddddddddddddd"
	const wrongCommit = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	mock := newMockRESTClient(t).
		withJSON("repos/actions/checkout/releases?per_page=100&page=1", []map[string]interface{}{
			{"tag_name": "v5.0.0", "prerelease": false},
		}).
		withJSON("repos/actions/checkout/git/ref/tags/v5.0.0", map[string]interface{}{
			"object": map[string]interface{}{
				"sha":  correctCommit,
				"type": "commit",
			},
		})

	t.Run("match", func(t *testing.T) {
		wf := buildWorkflowFile(t, `      - uses: actions/checkout@`+correctCommit+` # v5.0.0`)
		if exit := runVerify(mock, []*WorkflowFile{wf}); exit != 0 {
			t.Fatalf("runVerify exit = %d, want 0", exit)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		wf := buildWorkflowFile(t, `      - uses: actions/checkout@`+wrongCommit+` # v5.0.0`)
		if exit := runVerify(mock, []*WorkflowFile{wf}); exit == 0 {
			t.Fatal("expected runVerify to report mismatch")
		}
	})
}

func buildWorkflowFile(t *testing.T, line string) *WorkflowFile {
	t.Helper()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "workflow.yml")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("failed to seed workflow file: %v", err)
	}

	usage, ok := parseUsesLine(line)
	if !ok {
		t.Fatalf("parseUsesLine failed for %q", line)
	}
	wf := &WorkflowFile{
		Path:  path,
		Lines: []string{line},
		Uses:  []*ActionUsage{usage},
	}
	usage.File = wf
	usage.Line = 0
	return wf
}

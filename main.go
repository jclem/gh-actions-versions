package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
)

type restClient interface {
	Get(path string, response interface{}) error
}

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "verify":
		exit := cmdVerify(args)
		os.Exit(exit)
	case "fix":
		exit := cmdFix(args)
		os.Exit(exit)
	case "upgrade":
		exit := cmdUpgrade(args)
		os.Exit(exit)
	case "update":
		exit := cmdUpdate(args)
		os.Exit(exit)
	case "--help", "-h", "help":
		printHelp()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printHelp()
		os.Exit(1)
	}
}

func cmdVerify(args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "verify does not accept additional arguments\n")
		return 1
	}

	files, err := loadWorkflowFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load workflow files: %v\n", err)
		return 1
	}

	if len(allUsages(files)) == 0 {
		fmt.Println("No workflow or composite action usages found.")
		return 0
	}

	client, err := api.DefaultRESTClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create GitHub client: %v\n", err)
		return 1
	}

	exit := runVerify(client, files)
	return exit
}

func cmdFix(args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "fix does not accept additional arguments\n")
		return 1
	}

	files, err := loadWorkflowFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load workflow files: %v\n", err)
		return 1
	}

	if len(allUsages(files)) == 0 {
		fmt.Println("No workflow or composite action usages found.")
		return 0
	}

	client, err := api.DefaultRESTClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create GitHub client: %v\n", err)
		return 1
	}

	exit := runFix(client, files)
	return exit
}

func cmdUpgrade(args []string) int {
	client, err := api.DefaultRESTClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create GitHub client: %v\n", err)
		return 1
	}

	files, err := loadWorkflowFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load workflow files: %v\n", err)
		return 1
	}

	if len(allUsages(files)) == 0 {
		fmt.Println("No workflow or composite action usages found.")
		return 0
	}

	exit := runUpgrade(client, files, args)
	return exit
}

func cmdUpdate(args []string) int {
	client, err := api.DefaultRESTClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create GitHub client: %v\n", err)
		return 1
	}

	files, err := loadWorkflowFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load workflow files: %v\n", err)
		return 1
	}

	if len(allUsages(files)) == 0 {
		fmt.Println("No workflow or composite action usages found.")
		return 0
	}

	exit := runUpdate(client, files, args)
	return exit
}

func printHelp() {
	fmt.Println(`Usage: gh actions-versions <command> [flags]

Commands:
  verify            Ensure actions use full commit SHAs that match their tagged versions.
  fix               Pin actions to commit SHAs based on their tagged versions.
  upgrade [repo]    Upgrade one action (owner/repo) or all actions to the latest release.
  update [repo]     Refresh pinned commits to the latest release that matches current version spec.

Upgrade flags:
  --all             Upgrade every referenced action to its latest release tag.
  --version <tag>   Upgrade to a specific release tag (only with a single repo argument).

Update flags:
  --all             Update every referenced action to match its existing version spec.`)
}

type WorkflowFile struct {
	Path    string
	Lines   []string
	Uses    []*ActionUsage
	changed bool
}

func (wf *WorkflowFile) Save() error {
	if !wf.changed {
		return nil
	}
	content := strings.Join(wf.Lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return os.WriteFile(wf.Path, []byte(content), 0o644)
}

type ActionSpec struct {
	Owner string
	Repo  string
	Path  string
}

func (s ActionSpec) RepoKey() string {
	return strings.ToLower(fmt.Sprintf("%s/%s", s.Owner, s.Repo))
}

func (s ActionSpec) FullPath() string {
	base := fmt.Sprintf("%s/%s", s.Owner, s.Repo)
	if s.Path != "" {
		base += "/" + s.Path
	}
	return base
}

type ActionUsage struct {
	File       *WorkflowFile
	Line       int
	Indent     string
	Separator  string
	Quoted     bool
	Spec       ActionSpec
	Ref        string
	Comment    string
	RawComment string
}

func (u *ActionUsage) LineNumber() int {
	return u.Line + 1
}

func (u *ActionUsage) Set(ref, comment string) {
	value := fmt.Sprintf("%s@%s", u.Spec.FullPath(), strings.ToLower(ref))
	if u.Quoted {
		value = fmt.Sprintf("%q", value)
	}
	sep := u.Separator
	if sep == "" {
		sep = " "
	}
	line := fmt.Sprintf("%suses:%s%s", u.Indent, sep, value)
	if comment != "" {
		line = fmt.Sprintf("%s # %s", line, comment)
	}
	u.File.Lines[u.Line] = line
	u.File.changed = true
	u.Ref = strings.ToLower(ref)
	u.Comment = comment
	u.RawComment = comment
}

type TagResolver struct {
	client restClient
	cache  map[string]string
	spec   map[string]specResolution
}

type specResolution struct {
	tag    string
	commit string
}

func NewTagResolver(client restClient) *TagResolver {
	return &TagResolver{
		client: client,
		cache:  make(map[string]string),
		spec:   make(map[string]specResolution),
	}
}

func (r *TagResolver) Resolve(owner, repo, reference string) (string, error) {
	if isFullCommitSHA(reference) {
		return strings.ToLower(reference), nil
	}
	cacheKey := fmt.Sprintf("%s/%s@%s", strings.ToLower(owner), strings.ToLower(repo), reference)
	if sha, ok := r.cache[cacheKey]; ok {
		return sha, nil
	}

	pathRef := strings.ReplaceAll(url.PathEscape(reference), "%2F", "/")
	refEndpoint := fmt.Sprintf("repos/%s/%s/git/ref/tags/%s", owner, repo, pathRef)
	var refResponse struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err := r.client.Get(refEndpoint, &refResponse); err != nil {
		return "", err
	}

	currentSHA := refResponse.Object.SHA
	objectType := refResponse.Object.Type

	for objectType == "tag" {
		tagEndpoint := fmt.Sprintf("repos/%s/%s/git/tags/%s", owner, repo, currentSHA)
		var tagResponse struct {
			Object struct {
				SHA  string `json:"sha"`
				Type string `json:"type"`
			} `json:"object"`
		}
		if err := r.client.Get(tagEndpoint, &tagResponse); err != nil {
			return "", err
		}
		currentSHA = tagResponse.Object.SHA
		objectType = tagResponse.Object.Type
	}

	if objectType != "commit" {
		return "", fmt.Errorf("tag %s resolved to unsupported type %s", reference, objectType)
	}

	lowered := strings.ToLower(currentSHA)
	r.cache[cacheKey] = lowered
	return lowered, nil
}

func (r *TagResolver) ResolveSpec(owner, repo, spec string) (string, string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", fmt.Errorf("empty version specification")
	}

	cacheKey := fmt.Sprintf("%s/%s#%s", strings.ToLower(owner), strings.ToLower(repo), strings.ToLower(spec))
	if cached, ok := r.spec[cacheKey]; ok {
		return cached.tag, cached.commit, nil
	}

	kind, normalized := classifyVersionSpec(spec)

	var tag string
	var commit string
	var err error

	switch kind {
	case specExact:
		tag, commit, err = r.resolveExactSpec(owner, repo, spec, normalized)
	case specMinor, specMajor:
		tag, err = r.findLatestMatchingTag(owner, repo, normalized, kind)
		if err == nil {
			commit, err = r.Resolve(owner, repo, tag)
		}
	default:
		tag = spec
		commit, err = r.Resolve(owner, repo, spec)
	}

	if err != nil {
		return "", "", err
	}

	result := specResolution{tag: tag, commit: commit}
	r.spec[cacheKey] = result
	return result.tag, result.commit, nil
}

func (r *TagResolver) resolveExactSpec(owner, repo, original, normalized string) (string, string, error) {
	candidates := []string{}
	if normalized != "" {
		candidates = append(candidates, normalized)
	}
	if !strings.EqualFold(original, normalized) {
		candidates = append(candidates, original)
	}

	if trimmed := strings.TrimPrefix(strings.ToLower(original), "v"); trimmed != original {
		candidates = append(candidates, trimmed)
	}

	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		lower := strings.ToLower(candidate)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}

		commit, err := r.Resolve(owner, repo, candidate)
		if err == nil {
			return candidate, commit, nil
		}
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			continue
		}
		return "", "", err
	}
	return "", "", fmt.Errorf("no release found for %s/%s with tag %s", owner, repo, original)
}

func (r *TagResolver) findLatestMatchingTag(owner, repo, normalized string, kind versionSpecKind) (string, error) {
	for page := 1; ; page++ {
		var releases []struct {
			TagName    string `json:"tag_name"`
			Prerelease bool   `json:"prerelease"`
		}
		path := fmt.Sprintf("repos/%s/%s/releases?per_page=%d&page=%d", owner, repo, listPageSize, page)
		if err := r.client.Get(path, &releases); err != nil {
			var httpErr *api.HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
				break
			}
			return "", err
		}
		if len(releases) == 0 {
			break
		}
		for _, release := range releases {
			if release.Prerelease {
				continue
			}
			if matchVersionSpec(release.TagName, normalized, kind) {
				return release.TagName, nil
			}
		}
		if len(releases) < listPageSize {
			break
		}
	}

	for page := 1; ; page++ {
		var tags []struct {
			Name string `json:"name"`
		}
		path := fmt.Sprintf("repos/%s/%s/tags?per_page=%d&page=%d", owner, repo, listPageSize, page)
		if err := r.client.Get(path, &tags); err != nil {
			return "", err
		}
		if len(tags) == 0 {
			break
		}
		for _, tag := range tags {
			if matchVersionSpec(tag.Name, normalized, kind) {
				return tag.Name, nil
			}
		}
		if len(tags) < listPageSize {
			break
		}
	}

	return "", fmt.Errorf("no release found matching %s for %s/%s", normalized, owner, repo)
}

const listPageSize = 100

type versionSpecKind int

const (
	specUnknown versionSpecKind = iota
	specExact
	specMinor
	specMajor
)

var (
	commitSHARE   = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	semverExactRE = regexp.MustCompile(`^[vV]?\d+\.\d+\.\d+([-\+][0-9A-Za-z\.-]+)?$`)
	semverMinorRE = regexp.MustCompile(`^[vV]?\d+\.\d+$`)
	semverMajorRE = regexp.MustCompile(`^[vV]?\d+$`)
)

func classifyVersionSpec(spec string) (versionSpecKind, string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return specUnknown, ""
	}

	lower := strings.ToLower(spec)
	switch {
	case semverExactRE.MatchString(lower):
		return specExact, ensureLeadingV(lower)
	case semverMinorRE.MatchString(lower):
		return specMinor, ensureLeadingV(lower)
	case semverMajorRE.MatchString(lower):
		return specMajor, ensureLeadingV(lower)
	default:
		return specUnknown, spec
	}
}

func ensureLeadingV(spec string) string {
	if strings.HasPrefix(spec, "v") {
		return spec
	}
	if strings.HasPrefix(spec, "V") {
		return "v" + spec[1:]
	}
	if len(spec) > 0 && spec[0] >= '0' && spec[0] <= '9' {
		return "v" + spec
	}
	return spec
}

func matchVersionSpec(tag, normalized string, kind versionSpecKind) bool {
	tagLower := strings.ToLower(tag)
	normalizedLower := strings.ToLower(normalized)
	tagTrimmed := strings.TrimPrefix(tagLower, "v")
	specTrimmed := strings.TrimPrefix(normalizedLower, "v")

	switch kind {
	case specExact:
		return tagTrimmed == specTrimmed
	case specMinor, specMajor:
		if strings.HasPrefix(tagTrimmed, specTrimmed+".") {
			return true
		}
		return tagTrimmed == specTrimmed
	default:
		return tagLower == normalizedLower
	}
}

type Issue struct {
	File    string
	Line    int
	Message string
}

func runVerify(client restClient, files []*WorkflowFile) int {
	resolver := NewTagResolver(client)
	var issues []Issue

	for _, file := range files {
		for _, usage := range file.Uses {
			ref := usage.Ref
			if !isFullCommitSHA(ref) {
				issues = append(issues, Issue{
					File:    file.Path,
					Line:    usage.LineNumber(),
					Message: fmt.Sprintf("uses %s is not pinned to a full commit SHA (%s)", usage.Spec.FullPath(), ref),
				})
				continue
			}

			version, _ := splitComment(usage.Comment)
			if version == "" {
				issues = append(issues, Issue{
					File:    file.Path,
					Line:    usage.LineNumber(),
					Message: fmt.Sprintf("uses %s is missing a version comment", usage.Spec.FullPath()),
				})
				continue
			}

			tag, commit, err := resolver.ResolveSpec(usage.Spec.Owner, usage.Spec.Repo, version)
			if err != nil {
				issues = append(issues, Issue{
					File:    file.Path,
					Line:    usage.LineNumber(),
					Message: fmt.Sprintf("failed to resolve %s spec %s: %v", usage.Spec.FullPath(), version, err),
				})
				continue
			}

			if !strings.EqualFold(commit, ref) {
				issues = append(issues, Issue{
					File: file.Path,
					Line: usage.LineNumber(),
					Message: fmt.Sprintf("pinned SHA %s does not match %s (%s) for %s spec %s",
						ref, tag, commit, usage.Spec.FullPath(), version),
				})
			}
		}
	}

	if len(issues) > 0 {
		sort.SliceStable(issues, func(i, j int) bool {
			if issues[i].File == issues[j].File {
				return issues[i].Line < issues[j].Line
			}
			return issues[i].File < issues[j].File
		})
		for _, issue := range issues {
			fmt.Printf("%s:%d %s\n", issue.File, issue.Line, issue.Message)
		}
		return 1
	}

	fmt.Println("All workflows and composite actions are pinned to matching commit SHAs.")
	return 0
}

func runFix(client restClient, files []*WorkflowFile) int {
	resolver := NewTagResolver(client)
	var warnings []string
	var updated int
	var filesChanged int

	for _, file := range files {
		for _, usage := range file.Uses {
			ref := usage.Ref
			version, suffix := splitComment(usage.Comment)
			if version == "" {
				if isFullCommitSHA(ref) {
					continue
				}
				version = ref
				suffix = ""
			}

			_, commit, err := resolver.ResolveSpec(usage.Spec.Owner, usage.Spec.Repo, version)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s:%d unable to resolve %s version %s: %v",
					file.Path, usage.LineNumber(), usage.Spec.FullPath(), version, err))
				continue
			}

			newComment := joinComment(version, suffix)
			if strings.EqualFold(commit, ref) && strings.EqualFold(newComment, usage.Comment) {
				continue
			}

			usage.Set(commit, newComment)
			updated++
		}

		if file.changed {
			if err := file.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "failed to write %s: %v\n", file.Path, err)
				return 1
			}
			filesChanged++
		}
	}

	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, warning)
	}

	if updated == 0 {
		fmt.Println("No changes were required.")
		return 0
	}

	fmt.Printf("Updated %d action reference(s) across %d file(s).\n", updated, filesChanged)
	return 0
}

func runUpgrade(client restClient, files []*WorkflowFile, args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	all := fs.Bool("all", false, "upgrade all referenced actions")
	versionFlag := fs.String("version", "", "upgrade to a specific release tag")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *all && fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "upgrade --all does not accept a positional repository argument")
		return 1
	}

	if !*all && fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "upgrade requires an owner/repo argument unless --all is used")
		return 1
	}

	if *all && *versionFlag != "" {
		fmt.Fprintln(os.Stderr, "--version cannot be combined with --all")
		return 1
	}

	resolver := NewTagResolver(client)

	repoRecords := make(map[string]*repoRecord)
	var repoOrder []string

	for _, file := range files {
		for _, usage := range file.Uses {
			key := usage.Spec.RepoKey()
			record, exists := repoRecords[key]
			if !exists {
				record = &repoRecord{
					Owner:  usage.Spec.Owner,
					Repo:   usage.Spec.Repo,
					Usages: []*ActionUsage{},
				}
				repoRecords[key] = record
				repoOrder = append(repoOrder, key)
			}
			record.Usages = append(record.Usages, usage)
		}
	}

	if len(repoRecords) == 0 {
		fmt.Println("No remote actions to upgrade.")
		return 0
	}

	var totalUpdates int
	var filesChanged int

	applyRepo := func(record *repoRecord, targetVersion string) (int, error) {
		version, commit, err := determineVersion(client, resolver, record.Owner, record.Repo, targetVersion)
		if err != nil {
			return 0, err
		}

		var modified int
		for _, usage := range record.Usages {
			_, suffix := splitComment(usage.Comment)
			newComment := joinComment(version, suffix)
			if strings.EqualFold(usage.Ref, commit) && strings.EqualFold(usage.Comment, newComment) {
				continue
			}
			usage.Set(commit, newComment)
			modified++
		}

		if modified > 0 {
			fmt.Printf("Upgraded %s/%s to %s (%s).\n", record.Owner, record.Repo, version, shortSHA(commit))
		} else {
			fmt.Printf("%s/%s is already at %s (%s).\n", record.Owner, record.Repo, version, shortSHA(commit))
		}

		return modified, nil
	}

	targetRepos := repoOrder
	if !*all {
		target := strings.ToLower(fs.Arg(0))
		if strings.Count(target, "/") != 1 {
			fmt.Fprintln(os.Stderr, "repository argument must be in the form owner/repo")
			return 1
		}
		if _, ok := repoRecords[target]; !ok {
			fmt.Fprintf(os.Stderr, "repository %s not referenced in workflows or composite actions\n", target)
			return 1
		}
		targetRepos = []string{target}
	}

	for _, key := range targetRepos {
		record := repoRecords[key]
		modified, err := applyRepo(record, *versionFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to upgrade %s/%s: %v\n", record.Owner, record.Repo, err)
			return 1
		}
		totalUpdates += modified
	}

	for _, file := range files {
		if file.changed {
			if err := file.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "failed to write %s: %v\n", file.Path, err)
				return 1
			}
			filesChanged++
		}
	}

	if totalUpdates == 0 {
		fmt.Println("No changes were required.")
		return 0
	}

	fmt.Printf("Updated %d action reference(s) across %d file(s).\n", totalUpdates, filesChanged)
	return 0
}

func runUpdate(client restClient, files []*WorkflowFile, args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	all := fs.Bool("all", false, "update all referenced actions")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *all && fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "update --all does not accept a positional repository argument")
		return 1
	}

	if !*all && fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "update requires an owner/repo argument unless --all is used")
		return 1
	}

	resolver := NewTagResolver(client)
	targetRepo := ""
	if !*all {
		targetRepo = strings.ToLower(fs.Arg(0))
		if strings.Count(targetRepo, "/") != 1 {
			fmt.Fprintln(os.Stderr, "repository argument must be in the form owner/repo")
			return 1
		}
	}

	updateRecords := make(map[string]*updateRecord)
	var recordOrder []string
	var warnings []string
	var totalUpdates int
	var filesChanged int
	foundRepo := *all

	for _, file := range files {
		for _, usage := range file.Uses {
			repoKey := usage.Spec.RepoKey()
			if !*all && repoKey != targetRepo {
				continue
			}
			foundRepo = true

			version, suffix := splitComment(usage.Comment)
			if version == "" {
				warnings = append(warnings, fmt.Sprintf("%s:%d missing version comment for %s",
					file.Path, usage.LineNumber(), usage.Spec.FullPath()))
				continue
			}

			tag, commit, err := resolver.ResolveSpec(usage.Spec.Owner, usage.Spec.Repo, version)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s:%d unable to resolve %s spec %s: %v",
					file.Path, usage.LineNumber(), usage.Spec.FullPath(), version, err))
				continue
			}

			recordKey := fmt.Sprintf("%s|%s", repoKey, strings.ToLower(version))
			record, exists := updateRecords[recordKey]
			if !exists {
				record = &updateRecord{
					Owner: usage.Spec.Owner,
					Repo:  usage.Spec.Repo,
					Spec:  version,
				}
				updateRecords[recordKey] = record
				recordOrder = append(recordOrder, recordKey)
			}
			record.Tag = tag
			record.Commit = commit

			newComment := joinComment(version, suffix)
			if strings.EqualFold(commit, usage.Ref) && strings.EqualFold(newComment, usage.Comment) {
				record.Unchanged++
				continue
			}

			usage.Set(commit, newComment)
			record.Updated++
			totalUpdates++
		}

		if file.changed {
			if err := file.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "failed to write %s: %v\n", file.Path, err)
				return 1
			}
			filesChanged++
		}
	}

	if !foundRepo {
		fmt.Fprintf(os.Stderr, "repository %s not referenced in workflows or composite actions\n", targetRepo)
		return 1
	}

	sort.Strings(recordOrder)
	for _, key := range recordOrder {
		record := updateRecords[key]
		if record.Updated > 0 {
			fmt.Printf("Updated %s/%s spec %s to %s (%s).\n",
				record.Owner, record.Repo, record.Spec, record.Tag, shortSHA(record.Commit))
		} else {
			fmt.Printf("%s/%s spec %s already at %s (%s).\n",
				record.Owner, record.Repo, record.Spec, record.Tag, shortSHA(record.Commit))
		}
	}

	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, warning)
	}

	if totalUpdates == 0 {
		fmt.Println("No changes were required.")
		return 0
	}

	fmt.Printf("Updated %d action reference(s) across %d file(s).\n", totalUpdates, filesChanged)
	return 0
}

type updateRecord struct {
	Owner     string
	Repo      string
	Spec      string
	Tag       string
	Commit    string
	Updated   int
	Unchanged int
}

type repoRecord struct {
	Owner  string
	Repo   string
	Usages []*ActionUsage
}

func determineVersion(client restClient, resolver *TagResolver, owner, repo, override string) (string, string, error) {
	if override != "" {
		tag, commit, err := resolver.ResolveSpec(owner, repo, override)
		if err != nil {
			return "", "", err
		}
		return tag, commit, nil
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	err := client.Get(fmt.Sprintf("repos/%s/%s/releases/latest", owner, repo), &release)
	if err == nil && release.TagName != "" {
		commit, resolveErr := resolver.Resolve(owner, repo, release.TagName)
		if resolveErr == nil {
			return release.TagName, commit, nil
		}
		err = resolveErr
	}

	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode != 200 && httpErr.StatusCode != 0 {
		if httpErr.StatusCode != 404 {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}

	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if tagErr := client.Get(fmt.Sprintf("repos/%s/%s/tags?per_page=1", owner, repo), &tags); tagErr != nil {
		return "", "", tagErr
	}
	if len(tags) == 0 {
		return "", "", fmt.Errorf("no release or tag found for %s/%s", owner, repo)
	}
	return tags[0].Name, strings.ToLower(tags[0].Commit.SHA), nil
}

func loadWorkflowFiles() ([]*WorkflowFile, error) {
	var paths []string
	for _, root := range []struct {
		Path      string
		Predicate func(string) bool
	}{
		{
			Path: ".github/workflows",
			Predicate: func(path string) bool {
				return strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml")
			},
		},
		{
			Path: ".github/actions",
			Predicate: func(path string) bool {
				base := filepath.Base(path)
				if base != "action.yml" && base != "action.yaml" {
					return false
				}
				return true
			},
		},
	} {
		info, err := os.Stat(root.Path)
		if err != nil || !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(root.Path, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if root.Predicate(path) {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	sort.Strings(paths)

	var files []*WorkflowFile
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		lines := splitLines(string(content))
		wf := &WorkflowFile{
			Path:  path,
			Lines: lines,
			Uses:  []*ActionUsage{},
		}
		for idx, line := range lines {
			if usage, ok := parseUsesLine(line); ok {
				usage.File = wf
				usage.Line = idx
				wf.Uses = append(wf.Uses, usage)
			}
		}
		files = append(files, wf)
	}
	return files, nil
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

func isFullCommitSHA(ref string) bool {
	return commitSHARE.MatchString(ref)
}

func parseUsesLine(line string) (*ActionUsage, bool) {
	idx := strings.Index(line, "uses:")
	if idx < 0 {
		return nil, false
	}

	indent := line[:idx]
	after := line[idx+len("uses:"):]
	separator := after[:len(after)-len(strings.TrimLeft(after, " \t"))]
	rest := strings.TrimSpace(after)
	if rest == "" {
		return nil, false
	}

	valuePart, comment := splitValueAndComment(rest)
	if valuePart == "" {
		return nil, false
	}

	quoted := false
	if len(valuePart) >= 2 && ((valuePart[0] == '"' && valuePart[len(valuePart)-1] == '"') || (valuePart[0] == '\'' && valuePart[len(valuePart)-1] == '\'')) {
		quoted = true
		valuePart = valuePart[1 : len(valuePart)-1]
	}

	if strings.Contains(valuePart, "${{") {
		return nil, false
	}
	if strings.HasPrefix(valuePart, "./") || strings.HasPrefix(valuePart, "../") || strings.HasPrefix(valuePart, "/") {
		return nil, false
	}
	if strings.HasPrefix(valuePart, "docker://") {
		return nil, false
	}

	at := strings.LastIndex(valuePart, "@")
	if at < 0 {
		return nil, false
	}
	specPart := valuePart[:at]
	refPart := valuePart[at+1:]
	if refPart == "" {
		return nil, false
	}

	specPieces := strings.Split(specPart, "/")
	if len(specPieces) < 2 {
		return nil, false
	}
	owner := specPieces[0]
	repo := specPieces[1]
	if owner == "" || repo == "" {
		return nil, false
	}

	path := ""
	if len(specPieces) > 2 {
		path = strings.Join(specPieces[2:], "/")
	}

	return &ActionUsage{
		Indent:     indent,
		Separator:  separator,
		Quoted:     quoted,
		Spec:       ActionSpec{Owner: owner, Repo: repo, Path: path},
		Ref:        strings.ToLower(refPart),
		Comment:    strings.TrimSpace(comment),
		RawComment: strings.TrimSpace(comment),
	}, true
}

func splitValueAndComment(value string) (string, string) {
	inSingle := false
	inDouble := false
	for i, r := range value {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				valuePart := strings.TrimSpace(value[:i])
				comment := strings.TrimSpace(value[i+1:])
				return valuePart, comment
			}
		}
	}
	return strings.TrimSpace(value), ""
}

func splitComment(comment string) (string, string) {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return "", ""
	}
	fields := strings.Fields(comment)
	if len(fields) == 0 {
		return "", ""
	}
	version := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(comment, version))
	return version, rest
}

func joinComment(version, suffix string) string {
	if version == "" {
		return strings.TrimSpace(suffix)
	}
	if suffix == "" {
		return version
	}
	return fmt.Sprintf("%s %s", version, strings.TrimSpace(suffix))
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func allUsages(files []*WorkflowFile) []*ActionUsage {
	var result []*ActionUsage
	for _, file := range files {
		result = append(result, file.Uses...)
	}
	return result
}

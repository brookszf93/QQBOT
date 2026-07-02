package personalapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Service struct {
	root string
	mu   sync.Mutex
}

type Project struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type TodoItem struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	ProjectID string `json:"projectId,omitempty"`
	Status    string `json:"status"`
	DueAt     string `json:"dueAt,omitempty"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type MusicItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artist     string `json:"artist,omitempty"`
	URL        string `json:"url,omitempty"`
	Note       string `json:"note,omitempty"`
	Impression string `json:"impression,omitempty"`
	Status     string `json:"status"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

type NewsNote struct {
	ID        string   `json:"id"`
	Source    string   `json:"source,omitempty"`
	ArticleID int      `json:"articleId,omitempty"`
	Title     string   `json:"title"`
	URL       string   `json:"url,omitempty"`
	Takeaway  string   `json:"takeaway"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

type ActivityItem struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Text       string   `json:"text,omitempty"`
	URL        string   `json:"url,omitempty"`
	ProjectID  string   `json:"projectId,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	StartedAt  string   `json:"startedAt"`
	UpdatedAt  string   `json:"updatedAt"`
	FinishedAt string   `json:"finishedAt,omitempty"`
}

type stateFile struct {
	ActiveNovelProjectID string `json:"activeNovelProjectId,omitempty"`
	CurrentMusicID       string `json:"currentMusicId,omitempty"`
	CurrentActivityID    string `json:"currentActivityId,omitempty"`
}

type Screen struct {
	App        string             `json:"app"`
	State      stateFile          `json:"state"`
	Projects   []Project          `json:"projects,omitempty"`
	Todos      []TodoItem         `json:"todos,omitempty"`
	Music      []MusicItem        `json:"music,omitempty"`
	News       []NewsNote         `json:"news,omitempty"`
	Activities []ActivityItem     `json:"activities,omitempty"`
	Markdown   string             `json:"markdown,omitempty"`
	Workspace  *WorkspaceOverview `json:"workspace,omitempty"`
}

type NovelEntry struct {
	Project   Project `json:"project"`
	Active    bool    `json:"active"`
	Outline   string  `json:"outline,omitempty"`
	Notes     string  `json:"notes,omitempty"`
	Draft     string  `json:"draft,omitempty"`
	Journal   string  `json:"journal,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
}

type WorkspaceFile struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
	Preview   string `json:"preview,omitempty"`
}

type WorkspaceOverview struct {
	Root               string          `json:"root"`
	Sections           map[string]int  `json:"sections"`
	Recent             []WorkspaceFile `json:"recent"`
	CurrentActivity    *ActivityItem   `json:"currentActivity,omitempty"`
	ActiveNovelProject *Project        `json:"activeNovelProject,omitempty"`
	Scratchpad         string          `json:"scratchpad,omitempty"`
}

func NewService(root string) *Service {
	root = strings.TrimSpace(root)
	if root == "" {
		root = filepath.Join("data", "personal-apps")
	}
	return &Service{root: root}
}

func (s *Service) Root() string { return s.root }

func (s *Service) Screen(app string) (Screen, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return Screen{}, err
	}
	state, _ := s.loadState()
	screen := Screen{App: app}
	switch app {
	case "workspace":
		overview, _ := s.workspaceOverviewLocked()
		screen.Workspace = &overview
	case "todo":
		screen.Todos, _ = s.loadTodos()
	case "novel":
		screen.Projects, _ = s.loadProjects("novel")
		if cleanState, changed := s.cleanDanglingActiveNovelLocked(state, screen.Projects); changed {
			state = cleanState
			_ = s.saveState(state)
		}
		screen.Todos, _ = s.loadTodos()
		if state.ActiveNovelProjectID != "" {
			screen.Markdown = s.projectDigestLocked(state.ActiveNovelProjectID)
		}
	case "projects":
		screen.Projects, _ = s.loadProjects("")
		screen.Todos, _ = s.loadTodos()
	case "music":
		screen.Music, _ = s.loadMusic()
	case "news":
		screen.News, _ = s.loadNewsNotes()
	case "activity":
		screen.Activities, _ = s.loadActivities()
	case "browser":
		screen.Projects, _ = s.loadProjects("browser")
	}
	screen.State = state
	return screen, nil
}

func (s *Service) CreateProject(kind, title string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return Project{}, err
	}
	kind = cleanKind(kind)
	title = strings.TrimSpace(title)
	if title == "" {
		return Project{}, errors.New("title is required")
	}
	projects, _ := s.loadProjects("")
	project := Project{ID: uniqueID(kind, title), Title: title, Kind: kind, Status: "active", CreatedAt: nowText(), UpdatedAt: nowText()}
	projects = append(projects, project)
	if err := s.saveProjects(projects); err != nil {
		return Project{}, err
	}
	if err := os.MkdirAll(s.projectDir(project.ID), 0755); err != nil {
		return Project{}, err
	}
	_ = s.appendProjectFile(project.ID, "notes.md", "# Notes\n")
	_ = s.appendProjectFile(project.ID, "journal.md", "# Journal\n")
	if kind == "novel" {
		_ = s.appendProjectFile(project.ID, "outline.md", "# Outline\n")
		_ = s.appendProjectFile(project.ID, "draft.md", "# Draft\n")
		state, _ := s.loadState()
		if state.ActiveNovelProjectID == "" || !projectIDExists(projects, state.ActiveNovelProjectID) {
			state.ActiveNovelProjectID = project.ID
			_ = s.saveState(state)
		}
	}
	return project, nil
}

func (s *Service) ListProjects(kind string) ([]Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return nil, err
	}
	return s.loadProjects(cleanKind(kind))
}

func (s *Service) ListNovelEntries() ([]NovelEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return nil, err
	}
	state, _ := s.loadState()
	projects, _ := s.loadProjects("novel")
	if cleanState, changed := s.cleanDanglingActiveNovelLocked(state, projects); changed {
		state = cleanState
		_ = s.saveState(state)
	}
	entries := make([]NovelEntry, 0, len(projects))
	for _, project := range projects {
		entry := NovelEntry{
			Project: project,
			Active:  project.ID == state.ActiveNovelProjectID,
		}
		entry.Outline, entry.Truncated = s.readProjectFileForDashboardLocked(project.ID, "outline.md", entry.Truncated)
		entry.Notes, entry.Truncated = s.readProjectFileForDashboardLocked(project.ID, "notes.md", entry.Truncated)
		entry.Draft, entry.Truncated = s.readProjectFileForDashboardLocked(project.ID, "draft.md", entry.Truncated)
		entry.Journal, entry.Truncated = s.readProjectFileForDashboardLocked(project.ID, "journal.md", entry.Truncated)
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *Service) WorkspaceOverview() (WorkspaceOverview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return WorkspaceOverview{}, err
	}
	return s.workspaceOverviewLocked()
}

func (s *Service) WriteWorkspaceEntry(kind, title, text string, tags []string) (WorkspaceFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return WorkspaceFile{}, err
	}
	kind = cleanWorkspaceKind(kind)
	title = strings.TrimSpace(title)
	text = strings.TrimSpace(text)
	if title == "" {
		title = defaultWorkspaceTitle(kind)
	}
	if text == "" {
		return WorkspaceFile{}, errors.New("text is required")
	}
	dir := s.workspaceDir(kind)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return WorkspaceFile{}, err
	}
	name := uniqueID(kind, title) + ".md"
	path := filepath.Join(dir, name)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- 时间：%s\n", nowText())
	if cleanedTags := cleanTags(tags); len(cleanedTags) > 0 {
		fmt.Fprintf(&b, "- 标签：%s\n", strings.Join(cleanedTags, ", "))
	}
	b.WriteString("\n")
	b.WriteString(text)
	b.WriteString("\n")
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return WorkspaceFile{}, err
	}
	return s.workspaceFileFromPathLocked(kind, path), nil
}

func (s *Service) OpenNovelProject(projectID string) (Project, error) {
	project, _, err := s.OpenNovelProjectWithFallback(projectID)
	return project, err
}

func (s *Service) OpenNovelProjectWithFallback(projectID string) (Project, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return Project{}, false, err
	}
	requestedID := cleanID(projectID)
	project, err := s.findProject(requestedID)
	if err != nil {
		if active, ok := s.activeNovelProjectLocked(); ok {
			state, _ := s.loadState()
			state.ActiveNovelProjectID = active.ID
			_ = s.saveState(state)
			return active, requestedID != "" && requestedID != active.ID, nil
		}
		state, _ := s.loadState()
		if state.ActiveNovelProjectID == requestedID {
			state.ActiveNovelProjectID = ""
			_ = s.saveState(state)
		}
		return Project{}, false, err
	}
	if project.Kind != "novel" {
		return Project{}, false, fmt.Errorf("项目 %s 不是小说项目", projectID)
	}
	state, _ := s.loadState()
	state.ActiveNovelProjectID = project.ID
	return project, false, s.saveState(state)
}

func (s *Service) AppendProjectText(projectID, file, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return err
	}
	project, err := s.findProject(s.resolveProjectID(projectID))
	if err != nil {
		return err
	}
	file = allowedProjectFile(file)
	if file == "" {
		return errors.New("unsupported project file")
	}
	if err := s.appendProjectFile(project.ID, file, "\n"+strings.TrimSpace(text)+"\n"); err != nil {
		return err
	}
	return s.touchProject(project.ID)
}

func (s *Service) ResolveNovelProjectID(projectID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := cleanID(projectID)
	if id != "" {
		if project, err := s.findProject(id); err == nil && project.Kind == "novel" {
			return project.ID
		}
	}
	if active, ok := s.activeNovelProjectLocked(); ok {
		return active.ID
	}
	return id
}

func (s *Service) ReplaceProjectText(projectID, file, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return err
	}
	project, err := s.findProject(s.resolveProjectID(projectID))
	if err != nil {
		return err
	}
	file = allowedProjectFile(file)
	if file == "" {
		return errors.New("unsupported project file")
	}
	if err := os.MkdirAll(s.projectDir(project.ID), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.projectDir(project.ID), file), []byte(strings.TrimSpace(text)+"\n"), 0644); err != nil {
		return err
	}
	return s.touchProject(project.ID)
}

func (s *Service) AddTodo(text, projectID, dueAt string) (TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return TodoItem{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return TodoItem{}, errors.New("text is required")
	}
	items, _ := s.loadTodos()
	item := TodoItem{ID: uniqueID("todo", text), Text: text, ProjectID: strings.TrimSpace(projectID), DueAt: strings.TrimSpace(dueAt), Status: "open", CreatedAt: nowText(), UpdatedAt: nowText()}
	items = append(items, item)
	return item, s.saveTodos(items)
}

func (s *Service) ListTodos(status, projectID string) ([]TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return nil, err
	}
	items, _ := s.loadTodos()
	out := []TodoItem{}
	for _, item := range items {
		if status != "" && item.Status != status {
			continue
		}
		if projectID != "" && item.ProjectID != projectID {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) UpdateTodo(id, text, status, dueAt string) (TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, _ := s.loadTodos()
	for i := range items {
		if items[i].ID != id {
			continue
		}
		if strings.TrimSpace(text) != "" {
			items[i].Text = strings.TrimSpace(text)
		}
		if strings.TrimSpace(status) != "" {
			items[i].Status = strings.TrimSpace(status)
		}
		if dueAt != "" {
			items[i].DueAt = strings.TrimSpace(dueAt)
		}
		items[i].UpdatedAt = nowText()
		return items[i], s.saveTodos(items)
	}
	return TodoItem{}, fmt.Errorf("待办 %s 不存在", id)
}

func (s *Service) AddMusic(title, artist, url, note string) (MusicItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return MusicItem{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return MusicItem{}, errors.New("title is required")
	}
	items, _ := s.loadMusic()
	item := MusicItem{ID: uniqueID("music", title), Title: title, Artist: strings.TrimSpace(artist), URL: strings.TrimSpace(url), Note: strings.TrimSpace(note), Status: "queued", CreatedAt: nowText(), UpdatedAt: nowText()}
	items = append(items, item)
	return item, s.saveMusic(items)
}

func (s *Service) UpdateMusic(id, status, impression string) (MusicItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, _ := s.loadMusic()
	for i := range items {
		if items[i].ID != id {
			continue
		}
		if status != "" {
			items[i].Status = status
		}
		if impression != "" {
			items[i].Impression = strings.TrimSpace(impression)
		}
		items[i].UpdatedAt = nowText()
		if status == "current" {
			state, _ := s.loadState()
			state.CurrentMusicID = id
			_ = s.saveState(state)
		}
		return items[i], s.saveMusic(items)
	}
	return MusicItem{}, fmt.Errorf("音乐条目 %s 不存在", id)
}

func (s *Service) ListMusic() ([]MusicItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return nil, err
	}
	return s.loadMusic()
}

func (s *Service) AddNewsNote(source string, articleID int, title, url, takeaway string, tags []string) (NewsNote, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return NewsNote{}, err
	}
	title = strings.TrimSpace(title)
	takeaway = strings.TrimSpace(takeaway)
	if title == "" || takeaway == "" {
		return NewsNote{}, errors.New("title and takeaway are required")
	}
	items, _ := s.loadNewsNotes()
	item := NewsNote{ID: uniqueID("news", title), Source: strings.TrimSpace(source), ArticleID: articleID, Title: title, URL: strings.TrimSpace(url), Takeaway: takeaway, Tags: cleanTags(tags), CreatedAt: nowText(), UpdatedAt: nowText()}
	items = append(items, item)
	return item, s.saveNewsNotes(items)
}

func (s *Service) ListNewsNotes() ([]NewsNote, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return nil, err
	}
	return s.loadNewsNotes()
}

func (s *Service) StartActivity(kind, title, text, url string, tags []string) (ActivityItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return ActivityItem{}, err
	}
	kind = cleanActivityKind(kind)
	title = strings.TrimSpace(title)
	if title == "" {
		title = defaultActivityTitle(kind)
	}
	items, _ := s.loadActivities()
	now := nowText()
	item := ActivityItem{ID: uniqueID("activity-"+kind, title), Kind: kind, Title: title, Status: "active", Text: strings.TrimSpace(text), URL: strings.TrimSpace(url), Tags: cleanTags(tags), StartedAt: now, UpdatedAt: now}
	items = append(items, item)
	if err := s.saveActivities(items); err != nil {
		return ActivityItem{}, err
	}
	state, _ := s.loadState()
	state.CurrentActivityID = item.ID
	_ = s.saveState(state)
	return item, nil
}

func (s *Service) FinishActivity(id, text, projectID string) (ActivityItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, _ := s.loadActivities()
	if id = cleanID(id); id == "" {
		state, _ := s.loadState()
		id = state.CurrentActivityID
	}
	now := nowText()
	for i := range items {
		if items[i].ID != id {
			continue
		}
		items[i].Status = "done"
		items[i].UpdatedAt = now
		items[i].FinishedAt = now
		if strings.TrimSpace(text) != "" {
			if items[i].Text != "" {
				items[i].Text += "\n" + strings.TrimSpace(text)
			} else {
				items[i].Text = strings.TrimSpace(text)
			}
		}
		if strings.TrimSpace(projectID) != "" {
			items[i].ProjectID = cleanID(projectID)
		}
		state, _ := s.loadState()
		if state.CurrentActivityID == id {
			state.CurrentActivityID = ""
			_ = s.saveState(state)
		}
		return items[i], s.saveActivities(items)
	}
	return ActivityItem{}, fmt.Errorf("活动 %s 不存在", id)
}

func (s *Service) ListActivities(status, kind string) ([]ActivityItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return nil, err
	}
	items, _ := s.loadActivities()
	status = strings.TrimSpace(status)
	kind = cleanActivityKind(kind)
	out := []ActivityItem{}
	for _, item := range items {
		if status != "" && item.Status != status {
			continue
		}
		if kind != "" && item.Kind != kind {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) UpsertNovelEntry(projectID, title, text, file string) (Project, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return Project{}, false, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Project{}, false, errors.New("text is required")
	}
	file = allowedProjectFile(file)
	if file == "" {
		file = "draft.md"
	}
	project, created, err := s.resolveOrCreateNovelProjectLocked(projectID, title)
	if err != nil {
		return Project{}, false, err
	}
	if err := s.appendProjectFile(project.ID, file, "\n"+text+"\n"); err != nil {
		return Project{}, created, err
	}
	if err := s.touchProject(project.ID); err != nil {
		return Project{}, created, err
	}
	state, _ := s.loadState()
	state.ActiveNovelProjectID = project.ID
	_ = s.saveState(state)
	return project, created, nil
}

func (s *Service) ensure() error {
	for _, dir := range []string{s.root, s.projectsDir(), s.workspaceDir("journal"), s.workspaceDir("drafts"), s.workspaceDir("reading"), s.workspaceDir("music")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	scratchpad := s.scratchpadPath()
	if _, err := os.Stat(scratchpad); os.IsNotExist(err) {
		if err := os.WriteFile(scratchpad, []byte("# Scratchpad\n"), 0644); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) workspaceOverviewLocked() (WorkspaceOverview, error) {
	overview := WorkspaceOverview{
		Root:     s.root,
		Sections: map[string]int{},
	}
	for _, kind := range []string{"journal", "drafts", "reading", "music"} {
		files := s.listWorkspaceFilesLocked(kind)
		overview.Sections[kind] = len(files)
		overview.Recent = append(overview.Recent, files...)
	}
	sort.Slice(overview.Recent, func(i, j int) bool { return overview.Recent[i].UpdatedAt > overview.Recent[j].UpdatedAt })
	if len(overview.Recent) > 12 {
		overview.Recent = overview.Recent[:12]
	}
	if activities, err := s.loadActivities(); err == nil {
		for _, activity := range activities {
			if activity.Status == "active" {
				copied := activity
				overview.CurrentActivity = &copied
				break
			}
		}
	}
	if active, ok := s.activeNovelProjectLocked(); ok {
		copied := active
		overview.ActiveNovelProject = &copied
	}
	if data, err := os.ReadFile(s.scratchpadPath()); err == nil {
		overview.Scratchpad = trimWorkspacePreview(string(data), 1200)
	}
	return overview, nil
}

func (s *Service) listWorkspaceFilesLocked(kind string) []WorkspaceFile {
	dir := s.workspaceDir(kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := []WorkspaceFile{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		out = append(out, s.workspaceFileFromPathLocked(kind, filepath.Join(dir, entry.Name())))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

func (s *Service) workspaceFileFromPathLocked(kind, path string) WorkspaceFile {
	info, _ := os.Stat(path)
	updated := ""
	if info != nil {
		updated = info.ModTime().Format(time.RFC3339)
	}
	data, _ := os.ReadFile(path)
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			break
		}
	}
	rel, err := filepath.Rel(s.root, path)
	if err != nil {
		rel = path
	}
	return WorkspaceFile{
		Kind:      kind,
		Path:      filepath.ToSlash(rel),
		Title:     title,
		UpdatedAt: updated,
		Preview:   trimWorkspacePreview(string(data), 500),
	}
}

func (s *Service) projectDigestLocked(projectID string) string {
	var b strings.Builder
	for _, file := range []string{"outline.md", "notes.md", "draft.md", "journal.md"} {
		data, err := os.ReadFile(filepath.Join(s.projectDir(projectID), file))
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if len([]rune(text)) > 1800 {
			r := []rune(text)
			text = string(r[len(r)-1800:])
		}
		if text != "" {
			b.WriteString("\n## ")
			b.WriteString(file)
			b.WriteString("\n")
			b.WriteString(text)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func (s *Service) readProjectFileForDashboardLocked(projectID, file string, alreadyTruncated bool) (string, bool) {
	data, err := os.ReadFile(filepath.Join(s.projectDir(projectID), file))
	if err != nil {
		return "", alreadyTruncated
	}
	text := strings.TrimSpace(string(data))
	runes := []rune(text)
	const maxRunes = 60000
	if len(runes) > maxRunes {
		text = string(runes[len(runes)-maxRunes:])
		return text, true
	}
	return text, alreadyTruncated
}

func (s *Service) statePath() string           { return filepath.Join(s.root, "state.json") }
func (s *Service) todosPath() string           { return filepath.Join(s.root, "todos.json") }
func (s *Service) musicPath() string           { return filepath.Join(s.root, "music.json") }
func (s *Service) newsNotesPath() string       { return filepath.Join(s.root, "news-notes.json") }
func (s *Service) activitiesPath() string      { return filepath.Join(s.root, "activities.json") }
func (s *Service) projectsPath() string        { return filepath.Join(s.root, "projects.json") }
func (s *Service) projectsDir() string         { return filepath.Join(s.root, "projects") }
func (s *Service) projectDir(id string) string { return filepath.Join(s.projectsDir(), cleanID(id)) }
func (s *Service) workspaceDir(kind string) string {
	return filepath.Join(s.root, cleanWorkspaceKind(kind))
}
func (s *Service) scratchpadPath() string { return filepath.Join(s.root, "scratchpad.md") }

func (s *Service) loadState() (stateFile, error) {
	var state stateFile
	return state, readJSON(s.statePath(), &state)
}

func (s *Service) saveState(state stateFile) error { return writeJSON(s.statePath(), state) }

func (s *Service) loadProjects(kind string) ([]Project, error) {
	items := []Project{}
	if err := readJSON(s.projectsPath(), &items); err != nil {
		return nil, err
	}
	out := []Project{}
	for _, item := range items {
		if kind == "" || item.Kind == kind {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

func (s *Service) saveProjects(items []Project) error { return writeJSON(s.projectsPath(), items) }

func (s *Service) findProject(id string) (Project, error) {
	id = cleanID(id)
	projects, _ := s.loadProjects("")
	for _, project := range projects {
		if project.ID == id {
			return project, nil
		}
	}
	return Project{}, fmt.Errorf("项目 %s 不存在", id)
}

func (s *Service) activeNovelProjectLocked() (Project, bool) {
	state, _ := s.loadState()
	if state.ActiveNovelProjectID == "" {
		return Project{}, false
	}
	project, err := s.findProject(state.ActiveNovelProjectID)
	if err != nil || project.Kind != "novel" {
		return Project{}, false
	}
	return project, true
}

func (s *Service) resolveOrCreateNovelProjectLocked(projectID, title string) (Project, bool, error) {
	id := cleanID(projectID)
	if id != "" {
		if project, err := s.findProject(id); err == nil && project.Kind == "novel" {
			return project, false, nil
		}
	}
	if active, ok := s.activeNovelProjectLocked(); ok {
		return active, false, nil
	}
	title = strings.TrimSpace(title)
	projects, _ := s.loadProjects("")
	if title != "" {
		for _, project := range projects {
			if project.Kind == "novel" && strings.EqualFold(strings.TrimSpace(project.Title), title) {
				return project, false, nil
			}
		}
	}
	if title == "" {
		title = "随笔"
	}
	now := nowText()
	project := Project{ID: uniqueID("novel", title), Title: title, Kind: "novel", Status: "active", CreatedAt: now, UpdatedAt: now}
	projects = append(projects, project)
	if err := s.saveProjects(projects); err != nil {
		return Project{}, false, err
	}
	_ = s.appendProjectFile(project.ID, "outline.md", "# Outline\n")
	_ = s.appendProjectFile(project.ID, "notes.md", "# Notes\n")
	_ = s.appendProjectFile(project.ID, "draft.md", "# Draft\n")
	_ = s.appendProjectFile(project.ID, "journal.md", "# Journal\n")
	return project, true, nil
}

func (s *Service) resolveProjectID(id string) string {
	id = cleanID(id)
	if id != "" {
		return id
	}
	state, _ := s.loadState()
	if state.ActiveNovelProjectID != "" {
		projects, _ := s.loadProjects("novel")
		if !projectIDExists(projects, state.ActiveNovelProjectID) {
			state.ActiveNovelProjectID = ""
			_ = s.saveState(state)
			return ""
		}
	}
	return state.ActiveNovelProjectID
}

func (s *Service) cleanDanglingActiveNovelLocked(state stateFile, projects []Project) (stateFile, bool) {
	if state.ActiveNovelProjectID == "" || projectIDExists(projects, state.ActiveNovelProjectID) {
		return state, false
	}
	state.ActiveNovelProjectID = ""
	return state, true
}

func projectIDExists(projects []Project, id string) bool {
	id = cleanID(id)
	if id == "" {
		return false
	}
	for _, project := range projects {
		if project.ID == id {
			return true
		}
	}
	return false
}

func (s *Service) touchProject(id string) error {
	projects, _ := s.loadProjects("")
	for i := range projects {
		if projects[i].ID == id {
			projects[i].UpdatedAt = nowText()
			return s.saveProjects(projects)
		}
	}
	return nil
}

func (s *Service) appendProjectFile(projectID, file, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if err := os.MkdirAll(s.projectDir(projectID), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.projectDir(projectID), file), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return err
}

func (s *Service) loadTodos() ([]TodoItem, error) {
	items := []TodoItem{}
	return items, readJSON(s.todosPath(), &items)
}
func (s *Service) saveTodos(items []TodoItem) error { return writeJSON(s.todosPath(), items) }

func (s *Service) loadMusic() ([]MusicItem, error) {
	items := []MusicItem{}
	return items, readJSON(s.musicPath(), &items)
}
func (s *Service) saveMusic(items []MusicItem) error { return writeJSON(s.musicPath(), items) }

func (s *Service) loadNewsNotes() ([]NewsNote, error) {
	items := []NewsNote{}
	return items, readJSON(s.newsNotesPath(), &items)
}
func (s *Service) saveNewsNotes(items []NewsNote) error { return writeJSON(s.newsNotesPath(), items) }

func (s *Service) loadActivities() ([]ActivityItem, error) {
	items := []ActivityItem{}
	if err := readJSON(s.activitiesPath(), &items); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt > items[j].UpdatedAt })
	return items, nil
}
func (s *Service) saveActivities(items []ActivityItem) error {
	return writeJSON(s.activitiesPath(), items)
}

func readJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func nowText() string { return time.Now().Format(time.RFC3339) }

func cleanKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "project"
	}
	return cleanID(kind)
}

func cleanActivityKind(kind string) string {
	kind = cleanID(kind)
	switch kind {
	case "", "think", "idea", "note":
		return "thought"
	case "write", "writing", "novel", "essay":
		return "writing"
	case "browse", "browser", "web":
		return "browser"
	case "listen", "song", "music":
		return "music"
	case "read_news", "article", "news":
		return "news"
	case "todo", "task":
		return "todo"
	case "project", "work":
		return "project"
	default:
		return kind
	}
}

func defaultActivityTitle(kind string) string {
	switch kind {
	case "writing":
		return "写点东西"
	case "browser":
		return "浏览网页"
	case "music":
		return "听歌"
	case "news":
		return "看新闻"
	case "todo":
		return "整理待办"
	case "project":
		return "整理项目"
	default:
		return "想法"
	}
}

func cleanWorkspaceKind(kind string) string {
	switch cleanID(kind) {
	case "journal", "journals", "diary", "thought", "idea":
		return "journal"
	case "draft", "drafts", "writing", "novel", "essay":
		return "drafts"
	case "read", "reading", "news", "article":
		return "reading"
	case "music", "song", "listen":
		return "music"
	default:
		return "journal"
	}
}

func defaultWorkspaceTitle(kind string) string {
	switch cleanWorkspaceKind(kind) {
	case "drafts":
		return "一小段草稿"
	case "reading":
		return "阅读摘记"
	case "music":
		return "听歌记录"
	default:
		return "随手记"
	}
}

func trimWorkspacePreview(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

func uniqueID(prefix, text string) string {
	base := cleanID(text)
	if base == "" {
		base = prefix
	}
	if len(base) > 36 {
		base = base[:36]
	}
	return fmt.Sprintf("%s-%s-%d", cleanID(prefix), base, time.Now().UnixNano()%1000000)
}

var cleanIDRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func cleanID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, " ", "-")
	value = cleanIDRe.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-_")
	return strings.ToLower(value)
}

func allowedProjectFile(file string) string {
	switch strings.TrimSpace(file) {
	case "outline", "outline.md":
		return "outline.md"
	case "draft", "draft.md":
		return "draft.md"
	case "notes", "notes.md":
		return "notes.md"
	case "journal", "journal.md":
		return "journal.md"
	default:
		return ""
	}
}

func cleanTags(tags []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

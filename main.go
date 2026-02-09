package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
)

type Mode int

const (
	ModeInput Mode = iota
	ModeSessionList
	ModeQueue
	ModeNewSession
	ModeNewSessionLabel
	ModeRenameSession
	ModeConfirmDelete
	ModeCompleted
	ModeProjectList
	ModeNewProject
	ModeRenameProject
	ModeConfirmDeleteProject
	ModeDirList
	ModeNewDir
	ModeNewDirLabel
	ModeConfirmDeleteDir
	ModeConfirmDone
	ModeNewSubSession
	ModeEditGoal
)

type SessionStatus int

const (
	StatusIdle    SessionStatus = iota
	StatusRunning
	StatusWaiting
)

type QueueItemStatus int

const (
	QueuePending QueueItemStatus = iota
	QueueRunning
	QueueCompleted
)

type SubSession struct {
	ID             string        `json:"id"`
	Number         int           `json:"number"`
	Goal           string        `json:"goal"`
	History        []string      `json:"history"`
	Started        bool          `json:"started"`
	Status         SessionStatus `json:"status"`
	AIProvider     string        `json:"ai_provider"`
	HandoffSummary string        `json:"handoff_summary,omitempty"`
	GeminiStarted  bool          `json:"gemini_started"`
}

type Session struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Label       string       `json:"label"`
	SubSessions []SubSession `json:"subsessions"`
}

type QueueItem struct {
	ID            string          `json:"id"`
	SessionIdx    int             `json:"session_idx"`
	SubSessionIdx int             `json:"subsession_idx"`
	Prompt        string          `json:"prompt"`
	Status        QueueItemStatus `json:"status"`
	IsHandoff     bool            `json:"is_handoff,omitempty"`
	HandoffTarget string          `json:"handoff_target,omitempty"`
}

type DirEntry struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

type ClipboardBlock struct {
	Marker  string
	Content string
}

type Project struct {
	Name     string      `json:"name"`
	Sessions []Session   `json:"sessions"`
	Queue    []QueueItem `json:"queue"`
	Dirs     []DirEntry  `json:"dirs"`
}

type model struct {
	textInput            textarea.Model
	mode                 Mode
	projects             []Project
	currentProjectIdx    int
	sessions             []Session
	currentSessionIdx    int
	currentSubSessionIdx int
	listCursor           int
	queue                []QueueItem
	dirs                 []DirEntry
	thinkingDot          int
	width                int
	height               int
	deleteTarget         int
	completedSessionIdx  int
	completeCursor       int
	configDir            string
	// 디렉토리 자동완성
	dirSuggestions   []string
	dirSuggestionIdx int
	cwdPath          string
	lastDirInput     string
	pendingDirPath     string // 레이블 입력 대기 중인 디렉토리 경로
	pendingSessionName string // 설명 입력 대기 중인 세션 이름
	editingDirIdx      int    // 수정 중인 디렉토리 인덱스 (-1이면 새로 추가)
	editingSessionIdx  int    // 수정 중인 세션 인덱스 (-1이면 새로 추가)
	queueCursor        int       // Queue에서 선택된 항목 인덱스
	lastCtrlD          time.Time // 마지막 Ctrl+D 누른 시간
	// 명령어 자동완성
	cmdSuggestions   []string
	cmdSuggestionIdx int
	// 히스토리 스크롤
	historyScroll int
	// 클립보드 블록 (5줄 이상 붙여넣기)
	clipboardBlocks []ClipboardBlock
}

type thinkingTickMsg struct{}
type claudeResponseMsg struct {
	sessionIdx    int
	subSessionIdx int
	queueID       string
	response      string
}
type processQueueMsg struct{}

// 실행 중인 프로세스 추적
var runningProcess *exec.Cmd
var runningProcessMutex = &sync.Mutex{}

// 실행 중인 프로세스 종료
func killRunningProcess() {
	runningProcessMutex.Lock()
	defer runningProcessMutex.Unlock()
	if runningProcess != nil && runningProcess.Process != nil {
		runningProcess.Process.Kill()
		runningProcess = nil
	}
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("108"))

	projectNameStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("137"))

	promptTopStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("108")).
			Bold(true)

	promptArrowStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("180")).
				Bold(true)

	modeActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("150"))

	modeInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243"))

	userMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("180")).
			Bold(true)

	queueItemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250"))

	thinkingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Italic(true)

	listItemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	listSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("150")).
				Background(lipgloss.Color("236"))

	newSessionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("139")).
			Bold(true)

	deletePromptStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("167")).
				Bold(true)

	statusRunningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("208"))
	statusWaitingStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("179"))
	statusIdleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("108"))

	sessionTabActiveStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("150"))
	sessionTabInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243"))

	completeOptionStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))
	completeSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("150"))

	dirPathStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))
)

func getConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "leecli")
}

func (m *model) loadProjects() {
	configDir := m.configDir
	os.MkdirAll(configDir, 0755)

	indexPath := filepath.Join(configDir, "projects.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		m.projects = []Project{}
		return
	}

	var projectNames []string
	if err := json.Unmarshal(data, &projectNames); err != nil {
		m.projects = []Project{}
		return
	}

	m.projects = []Project{}
	for _, name := range projectNames {
		projectPath := filepath.Join(configDir, name+".json")
		projectData, err := os.ReadFile(projectPath)
		if err != nil {
			continue
		}
		var project Project
		if err := json.Unmarshal(projectData, &project); err != nil {
			continue
		}
		m.projects = append(m.projects, project)
	}
}

func (m *model) saveProjects() {
	configDir := m.configDir
	os.MkdirAll(configDir, 0755)

	if m.currentProjectIdx >= 0 && m.currentProjectIdx < len(m.projects) {
		m.projects[m.currentProjectIdx].Sessions = m.sessions
		m.projects[m.currentProjectIdx].Queue = m.queue
		m.projects[m.currentProjectIdx].Dirs = m.dirs
	}

	var projectNames []string
	for _, p := range m.projects {
		projectNames = append(projectNames, p.Name)
	}
	indexData, _ := json.MarshalIndent(projectNames, "", "  ")
	os.WriteFile(filepath.Join(configDir, "projects.json"), indexData, 0644)

	for _, project := range m.projects {
		projectData, _ := json.MarshalIndent(project, "", "  ")
		os.WriteFile(filepath.Join(configDir, project.Name+".json"), projectData, 0644)
	}
}

func (m *model) deleteProjectFile(name string) {
	projectPath := filepath.Join(m.configDir, name+".json")
	os.Remove(projectPath)
}

func (m *model) loadCurrentProject() {
	if m.currentProjectIdx >= 0 && m.currentProjectIdx < len(m.projects) {
		project := m.projects[m.currentProjectIdx]
		m.sessions = project.Sessions
		m.queue = project.Queue
		m.dirs = project.Dirs
		if m.dirs == nil {
			m.dirs = []DirEntry{}
		}
		// 서브세션 상태 복구
		for i := range m.sessions {
			if m.sessions[i].SubSessions == nil {
				m.sessions[i].SubSessions = []SubSession{}
			}
			for j := range m.sessions[i].SubSessions {
				if m.sessions[i].SubSessions[j].Status == StatusRunning {
					m.sessions[i].SubSessions[j].Status = StatusIdle
				}
			}
		}
		for i := range m.queue {
			if m.queue[i].Status == QueueRunning {
				m.queue[i].Status = QueuePending
			}
		}
		if len(m.sessions) > 0 {
			m.currentSessionIdx = 0
			if len(m.sessions[0].SubSessions) > 0 {
				m.currentSubSessionIdx = 0
			} else {
				m.currentSubSessionIdx = -1
			}
		} else {
			m.currentSessionIdx = -1
			m.currentSubSessionIdx = -1
		}
	}
}

func initialModel() model {
	ti := textarea.New()
	ti.Placeholder = ""
	ti.Focus()
	ti.CharLimit = 2000
	ti.SetWidth(60)
	ti.SetHeight(1)
	ti.ShowLineNumbers = false
	ti.Prompt = ""
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.FocusedStyle.Base = lipgloss.NewStyle()
	ti.BlurredStyle.Base = lipgloss.NewStyle()
	ti.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ti.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	ti.BlurredStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))

	cwd, _ := os.Getwd()

	m := model{
		textInput:            ti,
		mode:                 ModeProjectList,
		projects:             []Project{},
		currentProjectIdx:    -1,
		sessions:             []Session{},
		currentSessionIdx:    -1,
		currentSubSessionIdx: -1,
		listCursor:           0,
		queue:                []QueueItem{},
		dirs:                 []DirEntry{},
		width:                80,
		height:               24,
		configDir:            getConfigDir(),
		cwdPath:              cwd,
		dirSuggestions:       []string{},
		dirSuggestionIdx:     0,
		editingDirIdx:        -1,
		editingSessionIdx:    -1,
		cmdSuggestions:       []string{},
		cmdSuggestionIdx:     0,
	}

	m.loadProjects()

	if len(m.projects) == 0 {
		m.mode = ModeNewProject
		ti.Placeholder = "프로젝트 이름 입력..."
		m.textInput = ti
	}

	return m
}

func (m *model) updateSubSessionIdx() {
	if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
		session := m.sessions[m.currentSessionIdx]
		if len(session.SubSessions) > 0 {
			if m.currentSubSessionIdx < 0 || m.currentSubSessionIdx >= len(session.SubSessions) {
				m.currentSubSessionIdx = 0
			}
		} else {
			m.currentSubSessionIdx = -1
		}
	} else {
		m.currentSubSessionIdx = -1
	}
}

// getAIProvider는 서브세션의 AI 프로바이더를 반환합니다 (기본값: "claude")
func getAIProvider(sub *SubSession) string {
	if sub.AIProvider == "" {
		return "claude"
	}
	return sub.AIProvider
}

// getLastUserInput은 서브세션의 마지막 사용자 입력을 반환합니다
func (m *model) getLastUserInput() string {
	if m.currentSessionIdx < 0 || m.currentSessionIdx >= len(m.sessions) {
		return ""
	}
	session := m.sessions[m.currentSessionIdx]
	if m.currentSubSessionIdx < 0 || m.currentSubSessionIdx >= len(session.SubSessions) {
		return ""
	}
	subSession := session.SubSessions[m.currentSubSessionIdx]
	// History에서 마지막 "→ "로 시작하는 항목 찾기
	for i := len(subSession.History) - 1; i >= 0; i-- {
		if strings.HasPrefix(subSession.History[i], "→ ") {
			return strings.TrimPrefix(subSession.History[i], "→ ")
		}
	}
	return ""
}

// updatePlaceholder는 현재 서브세션의 마지막 입력을 placeholder로 설정합니다
func (m *model) updatePlaceholder() {
	lastInput := m.getLastUserInput()
	m.textInput.Placeholder = lastInput
}

func (m *model) updateCmdSuggestions() {
	input := m.textInput.Value()
	m.cmdSuggestions = nil
	m.cmdSuggestionIdx = 0

	if !strings.HasPrefix(input, "/") || len(input) < 2 {
		return
	}

	commands := []string{"/session", "/create", "/done", "/goal", "/dir", "/p", "/gemini", "/claude"}
	for _, cmd := range commands {
		if strings.HasPrefix(cmd, input) && cmd != input {
			m.cmdSuggestions = append(m.cmdSuggestions, cmd)
		}
	}
}

// findClipboardBlockAtCursor는 커서 위치에 클립보드 블록이 있으면 인덱스를 반환
func (m *model) findClipboardBlockAtCursor() int {
	value := m.textInput.Value()
	lines := strings.Split(value, "\n")
	curLine := m.textInput.Line()
	if curLine >= len(lines) {
		return -1
	}

	lineText := lines[curLine]
	charOffset := m.textInput.LineInfo().CharOffset

	for i, block := range m.clipboardBlocks {
		idx := strings.Index(lineText, block.Marker)
		if idx < 0 {
			continue
		}
		markerStart := len([]rune(lineText[:idx]))
		markerEnd := markerStart + len([]rune(block.Marker))
		if charOffset > markerStart && charOffset <= markerEnd {
			return i
		}
	}
	return -1
}

// removeClipboardBlock은 클립보드 블록을 텍스트에서 제거
func (m *model) removeClipboardBlock(idx int) {
	block := m.clipboardBlocks[idx]
	value := m.textInput.Value()
	newValue := strings.Replace(value, block.Marker, "", 1)
	m.textInput.SetValue(newValue)
	m.clipboardBlocks = append(m.clipboardBlocks[:idx], m.clipboardBlocks[idx+1:]...)
}

func (m *model) updateDirSuggestions() {
	input := m.textInput.Value()
	m.dirSuggestions = []string{}
	m.dirSuggestionIdx = 0

	// 현재 위치 추천 (등록 안 되어 있으면)
	cwdRegistered := false
	for _, d := range m.dirs {
		if d.Path == m.cwdPath {
			cwdRegistered = true
			break
		}
	}

	if input == "" {
		// 입력 없으면 현재 위치 추천
		if !cwdRegistered && m.cwdPath != "" {
			m.dirSuggestions = append(m.dirSuggestions, m.cwdPath)
		}
		home, _ := os.UserHomeDir()
		if home != "" && home != m.cwdPath {
			m.dirSuggestions = append(m.dirSuggestions, home)
		}
		return
	}

	// 경로 확장
	searchPath := input
	if strings.HasPrefix(searchPath, "~") {
		home, _ := os.UserHomeDir()
		searchPath = filepath.Join(home, searchPath[1:])
	}

	// 디렉토리 검색
	dir := searchPath
	prefix := ""
	if !strings.HasSuffix(searchPath, "/") {
		dir = filepath.Dir(searchPath)
		prefix = filepath.Base(searchPath)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue // 숨김 폴더 제외 (입력이 .으로 시작하지 않으면)
		}
		if prefix == "" || strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			fullPath := filepath.Join(dir, name)
			m.dirSuggestions = append(m.dirSuggestions, fullPath)
			if len(m.dirSuggestions) >= 10 {
				break
			}
		}
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, processQueueTick())
}

func processQueueTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return processQueueMsg{}
	})
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.textInput.SetWidth(msg.Width - 10)
		return m, nil

	case tea.MouseMsg:
		if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
			return m, nil
		}
		if len(m.sessions) == 0 {
			return m, nil
		}
		y := msg.Y
		x := msg.X

		// 세션 탭 줄 (y == 3)
		if y == 3 {
			pos := 2 // 앞 여백 "  "
			for i, session := range m.sessions {
				name := session.Name
				if len(name) > 10 {
					name = name[:10] + ".."
				}
				subCount := len(session.SubSessions)
				countStr := ""
				if subCount > 0 {
					countStr = fmt.Sprintf("(%d)", subCount)
				}
				tabWidth := lipgloss.Width(name)
				if countStr != "" {
					tabWidth += 1 + lipgloss.Width(countStr)
				}
				if x >= pos && x < pos+tabWidth {
					m.currentSessionIdx = i
					if len(session.SubSessions) > 0 {
						m.currentSubSessionIdx = 0
					}
					m.historyScroll = 0
					m.updatePlaceholder()
					return m, nil
				}
				pos += tabWidth + 5 // "  │  " 구분자
			}
			return m, nil
		}

		// 서브세션 탭 줄 (y == 5)
		if y == 5 {
			pos := 2 // 앞 여백 "  "
			for sessionIdx, session := range m.sessions {
				for subIdx := range session.SubSessions {
					numStr := fmt.Sprintf("#%d", session.SubSessions[subIdx].Number)
					tabWidth := lipgloss.Width(numStr) + 2 // numStr + " " + dot
					if x >= pos && x < pos+tabWidth {
						m.currentSessionIdx = sessionIdx
						m.currentSubSessionIdx = subIdx
						m.historyScroll = 0
						m.updatePlaceholder()
						return m, nil
					}
					pos += tabWidth + 2 // "  " 간격
				}
				// 세션 구분자 "│"
				if sessionIdx < len(m.sessions)-1 && len(session.SubSessions) > 0 {
					pos += 1 + 2 // "│" + "  " 간격
				}
			}
			return m, nil
		}

		return m, nil

	case processQueueMsg:
		cmds := []tea.Cmd{processQueueTick()}

		hasRunning := false
		for _, item := range m.queue {
			if item.Status == QueueRunning {
				hasRunning = true
				break
			}
		}

		if !hasRunning {
			for i := range m.queue {
				item := &m.queue[i]
				if item.Status != QueuePending {
					continue
				}

				if item.SessionIdx >= 0 && item.SessionIdx < len(m.sessions) {
					session := &m.sessions[item.SessionIdx]
					if item.SubSessionIdx >= 0 && item.SubSessionIdx < len(session.SubSessions) {
						subSession := &session.SubSessions[item.SubSessionIdx]

						item.Status = QueueRunning
						subSession.Status = StatusRunning

						if item.IsHandoff {
							// 인수인계: OLD provider로 요약 요청
							oldProvider := "claude"
							if item.HandoffTarget == "claude" {
								oldProvider = "gemini"
							}
							if oldProvider == "gemini" {
								cmds = append(cmds, runGemini(item.Prompt, false, item.SessionIdx, item.SubSessionIdx, item.ID, m.dirs, session.Label, subSession.Goal))
							} else {
								cmds = append(cmds, runClaude(item.Prompt, subSession.ID, false, item.SessionIdx, item.SubSessionIdx, item.ID, m.dirs, session.Label, subSession.Goal))
							}
						} else {
							// 일반 메시지: handoff summary가 있으면 첨부
							prompt := item.Prompt
							if subSession.HandoffSummary != "" {
								prompt = "[이전 AI 인수인계 요약]\n" + subSession.HandoffSummary + "\n\n" + prompt
								subSession.HandoffSummary = ""
							}

							provider := getAIProvider(subSession)
							if provider == "gemini" {
								isFirst := !subSession.GeminiStarted
								subSession.GeminiStarted = true
								cmds = append(cmds, runGemini(prompt, isFirst, item.SessionIdx, item.SubSessionIdx, item.ID, m.dirs, session.Label, subSession.Goal))
							} else {
								isFirst := !subSession.Started
								subSession.Started = true
								cmds = append(cmds, runClaude(prompt, subSession.ID, isFirst, item.SessionIdx, item.SubSessionIdx, item.ID, m.dirs, session.Label, subSession.Goal))
							}
						}
						break
					}
				}
			}
		}

		m.thinkingDot = (m.thinkingDot + 1) % 4
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		// Ctrl+D 두 번 눌러서 종료
		if msg.Type == tea.KeyCtrlD {
			now := time.Now()
			if now.Sub(m.lastCtrlD) < time.Second {
				killRunningProcess()
				return m, tea.Quit
			}
			m.lastCtrlD = now
			return m, nil
		}

		// Ctrl+V로 클립보드 붙여넣기 (Termux)
		if msg.Type == tea.KeyCtrlV {
			out, err := exec.Command("termux-clipboard-get").Output()
			if err == nil && len(out) > 0 {
				content := strings.TrimRight(string(out), "\n")
				lines := strings.Split(content, "\n")
				if len(lines) >= 5 {
					marker := fmt.Sprintf("[clipboard lines %d]", len(lines))
					m.clipboardBlocks = append(m.clipboardBlocks, ClipboardBlock{Marker: marker, Content: content})
					m.textInput.InsertString(marker)
				} else {
					m.textInput.InsertString(string(out))
				}
			}
			return m, nil
		}

		switch m.mode {
		case ModeNewDir:
			switch msg.Type {
			case tea.KeyCtrlC:
				killRunningProcess()
				return m, tea.Quit
			case tea.KeyEsc:
				// dirs가 비어있으면 나갈 수 없음
				if len(m.dirs) == 0 {
					return m, nil
				}
				m.mode = ModeDirList
				m.textInput.Placeholder = ""
				m.textInput.SetValue("")
				m.dirSuggestions = []string{}
				return m, nil
			case tea.KeyTab:
				// Tab으로 추천 선택 → 경로 + "/" 입력 → 하위 디렉토리 추천
				if len(m.dirSuggestions) > 0 {
					selected := m.dirSuggestions[m.dirSuggestionIdx]
					m.textInput.SetValue(selected + "/")
					m.dirSuggestionIdx = 0
					m.lastDirInput = selected + "/"
					m.updateDirSuggestions()
				}
				return m, nil
			case tea.KeyDown:
				if len(m.dirSuggestions) > 0 {
					m.dirSuggestionIdx = (m.dirSuggestionIdx + 1) % len(m.dirSuggestions)
				}
				return m, nil
			case tea.KeyUp:
				if len(m.dirSuggestions) > 0 {
					m.dirSuggestionIdx--
					if m.dirSuggestionIdx < 0 {
						m.dirSuggestionIdx = len(m.dirSuggestions) - 1
					}
				}
				return m, nil
			case tea.KeyEnter:
				dirPath := strings.TrimSpace(m.textInput.Value())
				if dirPath == "" {
					// 추천 항목이 있으면 첫 번째 선택
					if len(m.dirSuggestions) > 0 {
						dirPath = m.dirSuggestions[m.dirSuggestionIdx]
					} else {
						return m, nil
					}
				}
				// 경로 확장 (~를 홈으로)
				if strings.HasPrefix(dirPath, "~") {
					home, _ := os.UserHomeDir()
					dirPath = filepath.Join(home, dirPath[1:])
				}
				// 절대 경로로 변환
				absPath, err := filepath.Abs(dirPath)
				if err == nil {
					dirPath = absPath
				}
				// 중복 체크
				for _, d := range m.dirs {
					if d.Path == dirPath {
						m.textInput.SetValue("")
						m.updateDirSuggestions()
						return m, nil
					}
				}
				// 레이블 입력 모드로 전환
				m.pendingDirPath = dirPath
				m.textInput.SetValue("")
				m.textInput.Placeholder = "설명 입력 (선택사항)..."
				m.dirSuggestions = []string{}
				m.mode = ModeNewDirLabel
				return m, nil
			}

		case ModeNewDirLabel:
			switch msg.Type {
			case tea.KeyCtrlC:
				killRunningProcess()
				return m, tea.Quit
			case tea.KeyEsc:
				if m.editingDirIdx >= 0 {
					// 수정 취소
					m.editingDirIdx = -1
					m.pendingDirPath = ""
					m.textInput.SetValue("")
					m.textInput.Placeholder = ""
					m.mode = ModeDirList
				} else {
					// 새 디렉토리 - 레이블 없이 저장
					m.dirs = append(m.dirs, DirEntry{Path: m.pendingDirPath, Label: ""})
					m.pendingDirPath = ""
					m.textInput.SetValue("")
					m.textInput.Placeholder = ""
					m.saveProjects()
					if len(m.sessions) == 0 {
						m.mode = ModeNewSession
						m.textInput.Placeholder = "세션 이름 입력..."
					} else {
						m.mode = ModeDirList
						m.listCursor = len(m.dirs) - 1
					}
				}
				return m, nil
			case tea.KeyEnter:
				label := strings.TrimSpace(m.textInput.Value())
				if m.editingDirIdx >= 0 {
					// 기존 디렉토리 수정
					m.dirs[m.editingDirIdx].Label = label
					m.editingDirIdx = -1
					m.pendingDirPath = ""
					m.textInput.SetValue("")
					m.textInput.Placeholder = ""
					m.saveProjects()
					m.mode = ModeDirList
				} else {
					// 새 디렉토리 추가
					m.dirs = append(m.dirs, DirEntry{Path: m.pendingDirPath, Label: label})
					m.pendingDirPath = ""
					m.textInput.SetValue("")
					m.textInput.Placeholder = ""
					m.saveProjects()
					if len(m.sessions) == 0 {
						m.mode = ModeNewSession
						m.textInput.Placeholder = "세션 이름 입력..."
					} else {
						m.mode = ModeDirList
						m.listCursor = len(m.dirs) - 1
					}
				}
				return m, nil
			}

		case ModeConfirmDeleteDir:
			switch msg.String() {
			case "y", "Y":
				if m.deleteTarget >= 0 && m.deleteTarget < len(m.dirs) {
					m.dirs = append(m.dirs[:m.deleteTarget], m.dirs[m.deleteTarget+1:]...)
					if m.listCursor >= len(m.dirs)+1 {
						m.listCursor = len(m.dirs)
					}
					m.saveProjects()
				}
				m.mode = ModeDirList
				return m, nil
			case "n", "N", "esc":
				m.mode = ModeDirList
				return m, nil
			}
			return m, nil

		case ModeDirList:
			totalItems := len(m.dirs) + 1
			switch msg.String() {
			case "esc":
				// dirs가 비어있으면 나갈 수 없음
				if len(m.dirs) == 0 {
					return m, nil
				}
				m.updatePlaceholder()
				m.mode = ModeInput
				return m, nil
			case "j", "down":
				if m.listCursor < totalItems-1 {
					m.listCursor++
				}
				return m, nil
			case "k", "up":
				if m.listCursor > 0 {
					m.listCursor--
				}
				return m, nil
			case "enter", "l", "right":
				if m.listCursor == len(m.dirs) {
					m.mode = ModeNewDir
					m.editingDirIdx = -1
					m.textInput.SetValue("")
					m.textInput.Placeholder = "디렉토리 경로 입력..."
					m.updateDirSuggestions()
					return m, nil
				}
				return m, nil
			case "e":
				// 기존 디렉토리 설명 수정
				if m.listCursor < len(m.dirs) {
					m.editingDirIdx = m.listCursor
					m.pendingDirPath = m.dirs[m.listCursor].Path
					m.textInput.SetValue(m.dirs[m.listCursor].Label)
					m.textInput.Placeholder = "설명 입력 (선택사항)..."
					m.mode = ModeNewDirLabel
				}
				return m, nil
			case "x":
				if m.listCursor < len(m.dirs) {
					m.deleteTarget = m.listCursor
					m.mode = ModeConfirmDeleteDir
				}
				return m, nil
			}
			return m, nil

		case ModeNewProject:
			switch msg.Type {
			case tea.KeyCtrlC:
				killRunningProcess()
				return m, tea.Quit
			case tea.KeyEsc:
				if len(m.projects) > 0 {
					m.mode = ModeProjectList
					m.textInput.Placeholder = ""
					m.textInput.SetValue("")
				}
				return m, nil
			case tea.KeyEnter:
				name := strings.TrimSpace(m.textInput.Value())
				if name == "" {
					return m, nil
				}
				newProject := Project{
					Name:     name,
					Sessions: []Session{},
					Queue:    []QueueItem{},
					Dirs:     []DirEntry{},
				}
				m.projects = append(m.projects, newProject)
				m.currentProjectIdx = len(m.projects) - 1
				m.loadCurrentProject()
				m.textInput.SetValue("")
				m.textInput.Placeholder = ""
				// 새 프로젝트면 먼저 디렉토리 추가
				m.mode = ModeNewDir
				m.textInput.Placeholder = "디렉토리 경로 입력..."
				m.updateDirSuggestions()
				m.saveProjects()
				return m, nil
			}

		case ModeConfirmDeleteProject:
			switch msg.String() {
			case "y", "Y":
				if m.deleteTarget >= 0 && m.deleteTarget < len(m.projects) {
					deletedName := m.projects[m.deleteTarget].Name
					m.projects = append(m.projects[:m.deleteTarget], m.projects[m.deleteTarget+1:]...)
					m.deleteProjectFile(deletedName)
					if m.currentProjectIdx >= len(m.projects) {
						m.currentProjectIdx = len(m.projects) - 1
					}
					if m.listCursor >= len(m.projects)+1 {
						m.listCursor = len(m.projects)
					}
					m.saveProjects()
				}
				if len(m.projects) == 0 {
					m.mode = ModeNewProject
					m.textInput.Placeholder = "프로젝트 이름 입력..."
				} else {
					m.mode = ModeProjectList
				}
				return m, nil
			case "n", "N", "esc":
				m.mode = ModeProjectList
				return m, nil
			}
			return m, nil

		case ModeProjectList:
			totalItems := len(m.projects) + 1
			switch msg.String() {
			case "esc":
				if m.currentProjectIdx >= 0 {
					m.updatePlaceholder()
					m.mode = ModeInput
				}
				return m, nil
			case "j", "down":
				if m.listCursor < totalItems-1 {
					m.listCursor++
				}
				return m, nil
			case "k", "up":
				if m.listCursor > 0 {
					m.listCursor--
				}
				return m, nil
			case "enter", "l", "right":
				if m.listCursor == len(m.projects) {
					m.mode = ModeNewProject
					m.textInput.SetValue("")
					m.textInput.Placeholder = "프로젝트 이름 입력..."
					return m, nil
				} else {
					m.currentProjectIdx = m.listCursor
					m.loadCurrentProject()
					// dirs가 없으면 먼저 디렉토리 추가
					if len(m.dirs) == 0 {
						m.mode = ModeNewDir
						m.textInput.Placeholder = "디렉토리 경로 입력..."
						m.updateDirSuggestions()
					} else if len(m.sessions) == 0 {
						m.mode = ModeNewSession
						m.textInput.Placeholder = "세션 이름 입력..."
					} else {
						m.updatePlaceholder()
						m.mode = ModeInput
					}
					return m, nil
				}
			case "x":
				if m.listCursor < len(m.projects) {
					m.deleteTarget = m.listCursor
					m.mode = ModeConfirmDeleteProject
				}
				return m, nil
			case "r":
				// 프로젝트 이름 수정
				if m.listCursor < len(m.projects) {
					m.deleteTarget = m.listCursor // deleteTarget을 임시로 사용
					m.textInput.SetValue(m.projects[m.listCursor].Name)
					m.textInput.Placeholder = "새 이름 입력..."
					m.mode = ModeRenameProject
				}
				return m, nil
			}
			return m, nil

		case ModeRenameProject:
			switch msg.Type {
			case tea.KeyCtrlC:
				killRunningProcess()
				return m, tea.Quit
			case tea.KeyEsc:
				m.textInput.SetValue("")
				m.textInput.Placeholder = ""
				m.mode = ModeProjectList
				return m, nil
			case tea.KeyEnter:
				name := strings.TrimSpace(m.textInput.Value())
				if name != "" && m.deleteTarget >= 0 && m.deleteTarget < len(m.projects) {
					oldName := m.projects[m.deleteTarget].Name
					m.projects[m.deleteTarget].Name = name
					// 파일 이름도 변경
					m.deleteProjectFile(oldName)
					m.saveProjects()
				}
				m.textInput.SetValue("")
				m.textInput.Placeholder = ""
				m.mode = ModeProjectList
				return m, nil
			}

		case ModeNewSubSession:
			switch msg.Type {
			case tea.KeyCtrlC:
				killRunningProcess()
				return m, tea.Quit
			case tea.KeyEsc:
				m.textInput.SetValue("")
				m.updatePlaceholder()
				m.mode = ModeInput
				return m, nil
			case tea.KeyEnter:
				goal := strings.TrimSpace(m.textInput.Value())
				if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
					session := &m.sessions[m.currentSessionIdx]
					newNum := 1
					if len(session.SubSessions) > 0 {
						newNum = session.SubSessions[len(session.SubSessions)-1].Number + 1
					}
					newSubSession := SubSession{
						ID:      uuid.New().String(),
						Number:  newNum,
						Goal:    goal,
						History: []string{},
						Status:  StatusIdle,
					}
					session.SubSessions = append(session.SubSessions, newSubSession)
					m.currentSubSessionIdx = len(session.SubSessions) - 1
					m.saveProjects()
				}
				m.textInput.SetValue("")
				m.textInput.Placeholder = "" // 새 서브세션이라 history 없음
				m.mode = ModeInput
				return m, nil
			}

		case ModeEditGoal:
			switch msg.Type {
			case tea.KeyCtrlC:
				killRunningProcess()
				return m, tea.Quit
			case tea.KeyEsc:
				m.textInput.SetValue("")
				m.updatePlaceholder()
				m.mode = ModeInput
				return m, nil
			case tea.KeyEnter:
				goal := strings.TrimSpace(m.textInput.Value())
				if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
					session := &m.sessions[m.currentSessionIdx]
					if m.currentSubSessionIdx >= 0 && m.currentSubSessionIdx < len(session.SubSessions) {
						session.SubSessions[m.currentSubSessionIdx].Goal = goal
						m.saveProjects()
					}
				}
				m.textInput.SetValue("")
				m.updatePlaceholder()
				m.mode = ModeInput
				return m, nil
			}

		case ModeConfirmDone:
			switch msg.String() {
			case "y", "Y":
				// 서브세션 삭제
				if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
					session := &m.sessions[m.currentSessionIdx]
					if m.currentSubSessionIdx >= 0 && m.currentSubSessionIdx < len(session.SubSessions) {
						deleteIdx := m.currentSubSessionIdx
						session.SubSessions = append(session.SubSessions[:deleteIdx], session.SubSessions[deleteIdx+1:]...)
						// Number 재정렬 (1부터 순차적으로)
						for i := range session.SubSessions {
							session.SubSessions[i].Number = i + 1
						}
						// 큐에서 해당 서브세션 항목 제거 및 인덱스 조정
						newQueue := []QueueItem{}
						for _, item := range m.queue {
							if item.SessionIdx == m.currentSessionIdx && item.SubSessionIdx == deleteIdx {
								continue
							}
							if item.SessionIdx == m.currentSessionIdx && item.SubSessionIdx > deleteIdx {
								item.SubSessionIdx--
							}
							newQueue = append(newQueue, item)
						}
						m.queue = newQueue
						// 다른 서브세션으로 전환
						if len(session.SubSessions) == 0 {
							m.currentSubSessionIdx = -1
						} else {
							if m.currentSubSessionIdx >= len(session.SubSessions) {
								m.currentSubSessionIdx = len(session.SubSessions) - 1
							}
						}
						m.saveProjects()
					}
				}
				m.updatePlaceholder()
				m.mode = ModeInput
				return m, nil
			case "n", "N", "esc":
				m.updatePlaceholder()
				m.mode = ModeInput
				return m, nil
			}
			return m, nil

		case ModeConfirmDelete:
			switch msg.String() {
			case "y", "Y":
				if m.deleteTarget >= 0 && m.deleteTarget < len(m.sessions) {
					m.sessions = append(m.sessions[:m.deleteTarget], m.sessions[m.deleteTarget+1:]...)
					if m.currentSessionIdx >= len(m.sessions) {
						m.currentSessionIdx = len(m.sessions) - 1
					}
					if m.listCursor >= len(m.sessions)+1 {
						m.listCursor = len(m.sessions)
					}
					newQueue := []QueueItem{}
					for _, item := range m.queue {
						if item.SessionIdx == m.deleteTarget {
							continue
						}
						if item.SessionIdx > m.deleteTarget {
							item.SessionIdx--
						}
						newQueue = append(newQueue, item)
					}
					m.queue = newQueue
					m.saveProjects()
				}
				m.mode = ModeSessionList
				return m, nil
			case "n", "N", "esc":
				m.mode = ModeSessionList
				return m, nil
			}
			return m, nil

		case ModeSessionList:
			totalItems := len(m.sessions) + 1
			switch msg.String() {
			case "esc":
				if m.currentSessionIdx >= 0 {
					m.updatePlaceholder()
					m.mode = ModeInput
				}
				return m, nil
			case "tab":
				if m.currentSessionIdx >= 0 {
					m.updatePlaceholder()
					m.mode = ModeInput
				}
				return m, nil
			case "alt+q":
				m.mode = ModeQueue
				return m, nil
			case "j", "down":
				if m.listCursor < totalItems-1 {
					m.listCursor++
				}
				return m, nil
			case "k", "up":
				if m.listCursor > 0 {
					m.listCursor--
				}
				return m, nil
			case "h", "left":
				if m.currentSessionIdx >= 0 {
					m.updatePlaceholder()
					m.mode = ModeInput
				}
				return m, nil
			case "l", "right", "enter":
				if m.listCursor == len(m.sessions) {
					m.mode = ModeNewSession
					m.editingSessionIdx = -1
					m.textInput.SetValue("")
					m.textInput.Placeholder = "세션 이름 입력..."
					return m, nil
				} else {
					m.currentSessionIdx = m.listCursor
					m.updateSubSessionIdx()
					m.updatePlaceholder()
					m.mode = ModeInput
					return m, nil
				}
			case "x":
				if m.listCursor < len(m.sessions) {
					m.deleteTarget = m.listCursor
					m.mode = ModeConfirmDelete
				}
				return m, nil
			case "e":
				// 기존 세션 설명 수정
				if m.listCursor < len(m.sessions) {
					m.editingSessionIdx = m.listCursor
					m.pendingSessionName = m.sessions[m.listCursor].Name
					m.textInput.SetValue(m.sessions[m.listCursor].Label)
					m.textInput.Placeholder = "설명 입력 (선택사항)..."
					m.mode = ModeNewSessionLabel
				}
				return m, nil
			case "r":
				// 세션 이름 수정
				if m.listCursor < len(m.sessions) {
					m.editingSessionIdx = m.listCursor
					m.textInput.SetValue(m.sessions[m.listCursor].Name)
					m.textInput.Placeholder = "새 이름 입력..."
					m.mode = ModeRenameSession
				}
				return m, nil
			}
			return m, nil

		case ModeRenameSession:
			switch msg.Type {
			case tea.KeyCtrlC:
				killRunningProcess()
				return m, tea.Quit
			case tea.KeyEsc:
				m.editingSessionIdx = -1
				m.textInput.SetValue("")
				m.textInput.Placeholder = ""
				m.mode = ModeSessionList
				return m, nil
			case tea.KeyEnter:
				name := strings.TrimSpace(m.textInput.Value())
				if name != "" && m.editingSessionIdx >= 0 && m.editingSessionIdx < len(m.sessions) {
					m.sessions[m.editingSessionIdx].Name = name
					m.saveProjects()
				}
				m.editingSessionIdx = -1
				m.textInput.SetValue("")
				m.textInput.Placeholder = ""
				m.mode = ModeSessionList
				return m, nil
			}

		case ModeNewSession:
			switch msg.Type {
			case tea.KeyCtrlC:
				killRunningProcess()
				return m, tea.Quit
			case tea.KeyEsc:
				if len(m.sessions) > 0 {
					m.mode = ModeSessionList
					m.textInput.Placeholder = ""
				}
				return m, nil
			case tea.KeyEnter:
				name := strings.TrimSpace(m.textInput.Value())
				if name == "" {
					return m, nil
				}
				// 설명 입력 모드로 전환
				m.pendingSessionName = name
				m.textInput.SetValue("")
				m.textInput.Placeholder = "설명 입력 (선택사항)..."
				m.mode = ModeNewSessionLabel
				return m, nil
			}

		case ModeNewSessionLabel:
			switch msg.Type {
			case tea.KeyCtrlC:
				killRunningProcess()
				return m, tea.Quit
			case tea.KeyEsc:
				if m.editingSessionIdx >= 0 {
					// 수정 취소
					m.editingSessionIdx = -1
					m.pendingSessionName = ""
					m.textInput.SetValue("")
					m.textInput.Placeholder = ""
					m.mode = ModeSessionList
				} else {
					// 새 세션 - 설명 없이 저장
					newSession := Session{
						ID:          uuid.New().String(),
						Name:        m.pendingSessionName,
						Label:       "",
						SubSessions: []SubSession{},
					}
					m.sessions = append(m.sessions, newSession)
					m.currentSessionIdx = len(m.sessions) - 1
					m.currentSubSessionIdx = -1
					m.listCursor = m.currentSessionIdx
					m.pendingSessionName = ""
					m.textInput.SetValue("")
					m.textInput.Placeholder = "목적 입력..."
					m.mode = ModeNewSubSession // 서브세션 생성 유도
					m.saveProjects()
				}
				return m, nil
			case tea.KeyEnter:
				label := strings.TrimSpace(m.textInput.Value())
				if m.editingSessionIdx >= 0 {
					// 기존 세션 수정
					m.sessions[m.editingSessionIdx].Label = label
					m.editingSessionIdx = -1
					m.pendingSessionName = ""
					m.textInput.SetValue("")
					m.textInput.Placeholder = ""
					m.saveProjects()
					m.mode = ModeSessionList
				} else {
					// 새 세션 추가
					newSession := Session{
						ID:          uuid.New().String(),
						Name:        m.pendingSessionName,
						Label:       label,
						SubSessions: []SubSession{},
					}
					m.sessions = append(m.sessions, newSession)
					m.currentSessionIdx = len(m.sessions) - 1
					m.currentSubSessionIdx = -1
					m.listCursor = m.currentSessionIdx
					m.pendingSessionName = ""
					m.textInput.SetValue("")
					m.textInput.Placeholder = "목적 입력..."
					m.mode = ModeNewSubSession // 서브세션 생성 유도
					m.saveProjects()
				}
				return m, nil
			}

		case ModeQueue:
			// 활성 큐 항목 수 계산
			activeQueue := []int{}
			for i, item := range m.queue {
				if item.Status == QueuePending || item.Status == QueueRunning {
					activeQueue = append(activeQueue, i)
				}
			}
			queueLen := len(activeQueue)

			switch msg.Type {
			case tea.KeyCtrlC:
				killRunningProcess()
				return m, tea.Quit
			case tea.KeyEsc:
				m.updatePlaceholder()
				m.mode = ModeInput
				return m, nil
			}
			switch msg.String() {
			case "j", "down":
				if queueLen > 0 && m.queueCursor < queueLen-1 {
					m.queueCursor++
				}
				return m, nil
			case "k", "up":
				if m.queueCursor > 0 {
					m.queueCursor--
				}
				return m, nil
			case "h", "left", "alt+q":
				m.updatePlaceholder()
				m.mode = ModeInput
				return m, nil
			case "x":
				// 선택된 항목 삭제 (같은 서브세션의 관련 항목도 함께 삭제)
				if queueLen > 0 && m.queueCursor < queueLen {
					targetIdx := activeQueue[m.queueCursor]
					qItem := m.queue[targetIdx]
					sIdx := qItem.SessionIdx
					ssIdx := qItem.SubSessionIdx

					// 같은 서브세션의 모든 큐 항목 삭제 + 실행 중이면 프로세스 종료
					newQueue := []QueueItem{}
					for _, item := range m.queue {
						if item.SessionIdx == sIdx && item.SubSessionIdx == ssIdx {
							if item.Status == QueueRunning {
								killRunningProcess()
							}
							continue
						}
						newQueue = append(newQueue, item)
					}
					m.queue = newQueue

					// 서브세션 상태를 Idle로 복구
					if sIdx >= 0 && sIdx < len(m.sessions) {
						session := &m.sessions[sIdx]
						if ssIdx >= 0 && ssIdx < len(session.SubSessions) {
							session.SubSessions[ssIdx].Status = StatusIdle
						}
					}

					// 커서 조정
					newActiveCount := 0
					for _, item := range m.queue {
						if item.Status == QueuePending || item.Status == QueueRunning {
							newActiveCount++
						}
					}
					if m.queueCursor >= newActiveCount && m.queueCursor > 0 {
						m.queueCursor = newActiveCount - 1
					}
					if m.queueCursor < 0 {
						m.queueCursor = 0
					}
					m.saveProjects()
				}
				return m, nil
			case "enter":
				// 선택된 항목을 최우선으로 이동 (실행 중이면 취소 후 재시작)
				if queueLen > 0 && m.queueCursor < queueLen {
					targetIdx := activeQueue[m.queueCursor]
					// 현재 실행 중인 항목이 있으면 취소
					for i := range m.queue {
						if m.queue[i].Status == QueueRunning {
							killRunningProcess()
							m.queue[i].Status = QueuePending
							qItem := m.queue[i]
							if qItem.SessionIdx >= 0 && qItem.SessionIdx < len(m.sessions) {
								session := &m.sessions[qItem.SessionIdx]
								if qItem.SubSessionIdx >= 0 && qItem.SubSessionIdx < len(session.SubSessions) {
									session.SubSessions[qItem.SubSessionIdx].Status = StatusWaiting
								}
							}
						}
					}
					// 선택한 항목을 맨 앞으로
					if targetIdx > 0 {
						item := m.queue[targetIdx]
						m.queue = append(m.queue[:targetIdx], m.queue[targetIdx+1:]...)
						m.queue = append([]QueueItem{item}, m.queue...)
						m.queueCursor = 0
					}
					m.saveProjects()
				}
				return m, nil
			}

		case ModeInput:
			switch msg.Type {
			case tea.KeyCtrlC:
				killRunningProcess()
				return m, tea.Quit
			case tea.KeyBackspace:
				if len(m.clipboardBlocks) > 0 {
					blockIdx := m.findClipboardBlockAtCursor()
					if blockIdx >= 0 {
						m.removeClipboardBlock(blockIdx)
						return m, nil
					}
				}
				// 클립보드 블록이 아니면 기본 처리
				m.textInput, cmd = m.textInput.Update(msg)
				return m, cmd
			case tea.KeyLeft, tea.KeyRight, tea.KeyUp, tea.KeyDown:
				// Alt + 방향키는 무시
				if msg.Alt {
					return m, nil
				}
				// 방향키는 textInput으로 전달
				m.textInput, cmd = m.textInput.Update(msg)
				return m, cmd
			case tea.KeyRunes:
				// Alt + 키 처리
				if msg.Alt {
					switch string(msg.Runes) {
					case "l":
						// 다음 서브세션 (세션 넘어감, 순환)
						if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
							session := m.sessions[m.currentSessionIdx]
							// 현재 세션에 서브세션이 있고, 다음 서브세션으로 이동 가능
							if len(session.SubSessions) > 0 && m.currentSubSessionIdx >= 0 && m.currentSubSessionIdx < len(session.SubSessions)-1 {
								m.currentSubSessionIdx++
								m.historyScroll = 0
							} else {
								// 다음 세션의 첫 서브세션으로 (순환)
								found := false
								for i := m.currentSessionIdx + 1; i < len(m.sessions); i++ {
									if len(m.sessions[i].SubSessions) > 0 {
										m.currentSessionIdx = i
										m.currentSubSessionIdx = 0
										m.historyScroll = 0
										found = true
										break
									}
								}
								// 끝에 도달하면 처음부터 검색
								if !found {
									for i := 0; i <= m.currentSessionIdx; i++ {
										if len(m.sessions[i].SubSessions) > 0 {
											m.currentSessionIdx = i
											m.currentSubSessionIdx = 0
											m.historyScroll = 0
											break
										}
									}
								}
							}
							m.updatePlaceholder()
						}
						return m, nil
					case "h":
						// 이전 서브세션 (세션 넘어감, 순환)
						if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
							if m.currentSubSessionIdx > 0 {
								m.currentSubSessionIdx--
								m.historyScroll = 0
							} else {
								// 이전 세션의 마지막 서브세션으로 (순환)
								found := false
								for i := m.currentSessionIdx - 1; i >= 0; i-- {
									if len(m.sessions[i].SubSessions) > 0 {
										m.currentSessionIdx = i
										m.currentSubSessionIdx = len(m.sessions[i].SubSessions) - 1
										m.historyScroll = 0
										found = true
										break
									}
								}
								// 처음에 도달하면 끝에서부터 검색
								if !found {
									for i := len(m.sessions) - 1; i >= m.currentSessionIdx; i-- {
										if len(m.sessions[i].SubSessions) > 0 {
											m.currentSessionIdx = i
											m.currentSubSessionIdx = len(m.sessions[i].SubSessions) - 1
											m.historyScroll = 0
											break
										}
									}
								}
							}
							m.updatePlaceholder()
						}
						return m, nil
					case "q":
						m.mode = ModeQueue
						return m, nil
					case "k":
						// 히스토리 위로 스크롤
						var historyLen int
						if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
							session := m.sessions[m.currentSessionIdx]
							if m.currentSubSessionIdx >= 0 && m.currentSubSessionIdx < len(session.SubSessions) {
								historyLen = len(session.SubSessions[m.currentSubSessionIdx].History)
							}
						}
						taHeight := m.textInput.Height()
						if taHeight < 1 {
							taHeight = 1
						}
						historyHeight := m.height - 12 - taHeight
						if historyHeight < 3 {
							historyHeight = 3
						}
						maxScroll := historyLen - historyHeight
						if maxScroll < 0 {
							maxScroll = 0
						}
						if m.historyScroll < maxScroll {
							m.historyScroll++
						}
						return m, nil
					case "j":
						// 히스토리 아래로 스크롤 (최신으로)
						if m.historyScroll > 0 {
							m.historyScroll--
						}
						return m, nil
					case ",":
						// 히스토리 맨 아래로 (최신)
						m.historyScroll = 0
						return m, nil
					default:
						// 처리되지 않은 Alt 키 조합은 무시
						return m, nil
					}
				}
			}
			switch msg.String() {
			case "enter":
				// Enter = 전송
				input := strings.TrimSpace(m.textInput.Value())
				if input == "" {
					return m, nil
				}
				// 명령어 처리
				if input == "/session" || input == "/s" {
					m.textInput.SetValue("")
					m.cmdSuggestions = nil
					m.mode = ModeSessionList
					m.listCursor = m.currentSessionIdx
					return m, nil
				}
				if input == "/create" || input == "/c" {
					m.textInput.SetValue("")
					m.cmdSuggestions = nil
					if m.currentSessionIdx >= 0 {
						m.mode = ModeNewSubSession
						m.textInput.Placeholder = "목적 입력..."
					}
					return m, nil
				}
				if input == "/p" {
					m.textInput.SetValue("")
					m.cmdSuggestions = nil
					m.mode = ModeProjectList
					m.listCursor = m.currentProjectIdx
					return m, nil
				}
				if input == "/dir" {
					m.textInput.SetValue("")
					m.cmdSuggestions = nil
					m.mode = ModeDirList
					m.listCursor = 0
					return m, nil
				}
				if input == "/done" {
					m.textInput.SetValue("")
					m.cmdSuggestions = nil
					if m.currentSessionIdx >= 0 && m.currentSubSessionIdx >= 0 {
						m.mode = ModeConfirmDone
					}
					return m, nil
				}
				if input == "/gemini" || input == "/claude" {
					m.textInput.SetValue("")
					m.cmdSuggestions = nil
					if m.currentSessionIdx >= 0 && m.currentSubSessionIdx >= 0 {
						session := &m.sessions[m.currentSessionIdx]
						if m.currentSubSessionIdx < len(session.SubSessions) {
							sub := &session.SubSessions[m.currentSubSessionIdx]
							newProvider := "gemini"
							if input == "/claude" {
								newProvider = "claude"
							}
							oldProvider := getAIProvider(sub)
							if oldProvider == newProvider {
								return m, nil
							}

							// 이전 AI가 시작된 적 있으면 인수인계 요청
							oldStarted := (oldProvider == "claude" && sub.Started) || (oldProvider == "gemini" && sub.GeminiStarted)
							if oldStarted {
								queueID := uuid.New().String()[:8]
								m.queue = append(m.queue, QueueItem{
									ID:            queueID,
									SessionIdx:    m.currentSessionIdx,
									SubSessionIdx: m.currentSubSessionIdx,
									Prompt:        "다른 AI에게 인수인계한다. 최근 5개 대화를 중심으로 요약해줘. 이전 대화는 한줄씩, 마지막 대화는 자세하게(사용자 요청, 네 답변/처리 내용, 수정한 파일과 변경사항, 남은 작업 포함). 수정중인 프로젝트 경로와 파일 구조도 언급해.",
									Status:        QueuePending,
									IsHandoff:     true,
									HandoffTarget: newProvider,
								})
								sub.History = append(sub.History, fmt.Sprintf(">> %s → %s 인수인계 중...", oldProvider, newProvider))
								sub.Status = StatusWaiting
							}

							sub.AIProvider = newProvider
							m.saveProjects()
						}
					}
					return m, nil
				}
				if input == "/goal" {
					m.textInput.SetValue("")
					m.cmdSuggestions = nil
					if m.currentSessionIdx >= 0 && m.currentSubSessionIdx >= 0 {
						session := m.sessions[m.currentSessionIdx]
						if m.currentSubSessionIdx < len(session.SubSessions) {
							m.textInput.SetValue(session.SubSessions[m.currentSubSessionIdx].Goal)
							m.mode = ModeEditGoal
							m.textInput.Placeholder = "목적 입력..."
						}
					}
					return m, nil
				}
				// 서브세션 없으면 먼저 생성하게 유도
				if m.currentSessionIdx >= 0 && m.currentSubSessionIdx < 0 {
					m.textInput.SetValue("")
					m.cmdSuggestions = nil
					m.mode = ModeNewSubSession
					m.textInput.Placeholder = "목적 입력..."
					return m, nil
				}
				// 일반 프롬프트 전송
				if m.currentSessionIdx >= 0 && m.currentSubSessionIdx >= 0 {
					session := &m.sessions[m.currentSessionIdx]
					if m.currentSubSessionIdx < len(session.SubSessions) {
						subSession := &session.SubSessions[m.currentSubSessionIdx]
						queueID := uuid.New().String()[:8]
						// 클립보드 마커를 실제 내용으로 치환 (Claude에 보낼 프롬프트)
						prompt := input
						for _, block := range m.clipboardBlocks {
							prompt = strings.Replace(prompt, block.Marker, block.Content, 1)
						}
						m.queue = append(m.queue, QueueItem{
							ID:            queueID,
							SessionIdx:    m.currentSessionIdx,
							SubSessionIdx: m.currentSubSessionIdx,
							Prompt:        prompt,
							Status:        QueuePending,
						})
						// 히스토리에는 마커 형태로 표시
						subSession.History = append(subSession.History, fmt.Sprintf("→ [%s] %s", queueID, input))
						if subSession.Status == StatusIdle {
							subSession.Status = StatusWaiting
						}
						m.saveProjects()
						m.textInput.Placeholder = input // 방금 보낸 입력을 placeholder로
					}
				}
				m.textInput.SetValue("")
				m.cmdSuggestions = nil
				m.clipboardBlocks = nil
				return m, nil
			case "shift+enter":
				m.textInput.InsertString("\n")
				return m, nil
			case "tab":
				// 명령어 자동완성
				input := m.textInput.Value()
				if strings.HasPrefix(input, "/") && len(m.cmdSuggestions) > 0 {
					m.textInput.SetValue(m.cmdSuggestions[m.cmdSuggestionIdx])
					m.cmdSuggestions = nil
					return m, nil
				}
				// 세션 전환
				if len(m.sessions) > 1 {
					m.currentSessionIdx = (m.currentSessionIdx + 1) % len(m.sessions)
					m.updateSubSessionIdx()
					m.historyScroll = 0
					m.updatePlaceholder()
				}
				return m, nil
			case "shift+tab":
				if len(m.sessions) > 1 {
					m.currentSessionIdx--
					if m.currentSessionIdx < 0 {
						m.currentSessionIdx = len(m.sessions) - 1
					}
					m.updateSubSessionIdx()
					m.historyScroll = 0
					m.updatePlaceholder()
				}
				return m, nil
			}
		}

	case claudeResponseMsg:
		// handoff 여부 확인
		isHandoff := false
		for _, item := range m.queue {
			if item.ID == msg.queueID && item.IsHandoff {
				isHandoff = true
				break
			}
		}

		// 큐에서 완료된 항목 제거
		newQueue := []QueueItem{}
		for _, item := range m.queue {
			if item.ID == msg.queueID {
				continue
			}
			newQueue = append(newQueue, item)
		}
		m.queue = newQueue

		if msg.sessionIdx >= 0 && msg.sessionIdx < len(m.sessions) {
			session := &m.sessions[msg.sessionIdx]
			if msg.subSessionIdx >= 0 && msg.subSessionIdx < len(session.SubSessions) {
				subSession := &session.SubSessions[msg.subSessionIdx]

				if isHandoff {
					// 인수인계 응답: HandoffSummary에 저장
					subSession.HandoffSummary = strings.TrimSpace(msg.response)
					// "인수인계 중..." → "인수인계 완료" 로 교체
					for i := len(subSession.History) - 1; i >= 0; i-- {
						if strings.Contains(subSession.History[i], "인수인계 중...") {
							subSession.History[i] = strings.Replace(subSession.History[i], "인수인계 중...", "인수인계 완료", 1)
							break
						}
					}
				} else {
					// 일반 응답: 히스토리에 추가
					marker := fmt.Sprintf("→ [%s] ", msg.queueID)
					for i, histLine := range subSession.History {
						if strings.HasPrefix(histLine, marker) {
							subSession.History[i] = "→ " + histLine[len(marker):]
							break
						}
					}

					lines := strings.Split(strings.TrimSpace(msg.response), "\n")
					for _, line := range lines {
						subSession.History = append(subSession.History, line)
					}
				}
			}
		}

		// 모든 서브세션 상태 검증 및 동기화
		// 큐에 Running인 항목이 있는 서브세션만 Running 상태여야 함
		for i := range m.sessions {
			session := &m.sessions[i]
			for j := range session.SubSessions {
				subSession := &session.SubSessions[j]

				// 이 서브세션에 대한 큐 아이템 상태 확인
				hasRunning := false
				hasPending := false
				for _, item := range m.queue {
					if item.SessionIdx == i && item.SubSessionIdx == j {
						if item.Status == QueueRunning {
							hasRunning = true
						} else if item.Status == QueuePending {
							hasPending = true
						}
					}
				}

				// 상태 동기화
				prevStatus := subSession.Status
				if hasRunning {
					subSession.Status = StatusRunning
				} else if hasPending {
					subSession.Status = StatusWaiting
				} else {
					subSession.Status = StatusIdle
				}

				// 작업 완료 시 termux 알림
				if prevStatus != StatusIdle && subSession.Status == StatusIdle && i == msg.sessionIdx && j == msg.subSessionIdx {
					title := session.Name + " - " + subSession.Goal
					escapedTitle := strings.ReplaceAll(title, "'", "'\\''")
					escapedGroup := strings.ReplaceAll(session.Name, "'", "'\\''")
					go exec.Command("sh", "-c", "termux-notification -t '"+escapedTitle+"' -c '완료되었습니다!' --group '"+escapedGroup+"' --action 'am start -n com.termux/.HomeActivity'").Run()
				}
			}
		}

		m.saveProjects()
		return m, nil
	}

	m.textInput, cmd = m.textInput.Update(msg)

	// textarea 높이 동적 조절
	if m.mode == ModeInput {
		content := m.textInput.Value()
		width := m.textInput.Width()
		if width < 1 {
			width = 50
		}
		lines := 0
		for _, line := range strings.Split(content, "\n") {
			lineWidth := lipgloss.Width(line)
			if lineWidth == 0 {
				lines += 1
			} else {
				// 올림 나눗셈으로 줄 래핑 계산 (실제 터미널 폭 사용)
				lines += (lineWidth + width - 1) / width
			}
		}
		if lines < 1 {
			lines = 1
		}
		if lines > 10 {
			lines = 10
		}
		m.textInput.SetHeight(lines)
	}

	// ModeNewDir에서 입력이 변경되면 추천 업데이트
	if m.mode == ModeNewDir {
		currentInput := m.textInput.Value()
		if currentInput != m.lastDirInput {
			m.lastDirInput = currentInput
			m.updateDirSuggestions()
		}
	}

	// ModeInput에서 /로 시작하면 명령어 추천 업데이트
	if m.mode == ModeInput {
		m.updateCmdSuggestions()
	}

	return m, cmd
}

func (m model) getStatusDot(status SessionStatus) string {
	switch status {
	case StatusRunning:
		return statusRunningStyle.Render("●")
	case StatusWaiting:
		return statusWaitingStyle.Render("●")
	default:
		return statusIdleStyle.Render("●")
	}
}

func (m model) getSubSessionStatus(session Session) SessionStatus {
	// 서브세션들의 상태를 종합
	hasRunning := false
	hasWaiting := false
	for _, sub := range session.SubSessions {
		if sub.Status == StatusRunning {
			hasRunning = true
		} else if sub.Status == StatusWaiting {
			hasWaiting = true
		}
	}
	if hasRunning {
		return StatusRunning
	}
	if hasWaiting {
		return StatusWaiting
	}
	return StatusIdle
}

func (m model) viewSessionTabs() string {
	if len(m.sessions) == 0 {
		return ""
	}

	var b strings.Builder

	// 세션 탭
	var tabs []string
	for i, session := range m.sessions {
		name := session.Name
		if len(name) > 10 {
			name = name[:10] + ".."
		}

		// 서브세션 개수 표시
		subCount := len(session.SubSessions)
		countStr := ""
		if subCount > 0 {
			countStr = fmt.Sprintf("(%d)", subCount)
		}

		if i == m.currentSessionIdx {
			tabs = append(tabs, sessionTabActiveStyle.Render(name)+" "+modeInactiveStyle.Render(countStr))
		} else {
			tabs = append(tabs, sessionTabInactiveStyle.Render(name)+" "+modeInactiveStyle.Render(countStr))
		}
	}
	b.WriteString("  " + strings.Join(tabs, "  │  ") + "\n")

	// 모든 세션의 서브세션들 표시
	var subTabs []string
	for sessionIdx, session := range m.sessions {
		for subIdx, sub := range session.SubSessions {
			dot := m.getStatusDot(sub.Status)
			numStr := fmt.Sprintf("#%d", sub.Number)
			isSelected := sessionIdx == m.currentSessionIdx && subIdx == m.currentSubSessionIdx
			if isSelected {
				subTabs = append(subTabs, sessionTabActiveStyle.Render(numStr)+" "+dot)
			} else {
				subTabs = append(subTabs, modeInactiveStyle.Render(numStr)+" "+dot)
			}
		}
		// 세션 구분자 (마지막 세션 제외)
		if sessionIdx < len(m.sessions)-1 && len(session.SubSessions) > 0 {
			subTabs = append(subTabs, modeInactiveStyle.Render("│"))
		}
	}
	if len(subTabs) > 0 {
		b.WriteString("\n  " + strings.Join(subTabs, "  ") + "\n")
	}
	b.WriteString("\n")

	return b.String()
}

func (m model) getProjectName() string {
	if m.currentProjectIdx >= 0 && m.currentProjectIdx < len(m.projects) {
		return m.projects[m.currentProjectIdx].Name
	}
	return ""
}

func (m model) View() string {
	var b strings.Builder

	banner := titleStyle.Render("  ⚡ Lee CLI")
	projectName := m.getProjectName()
	if projectName != "" {
		banner += " " + projectNameStyle.Render("("+projectName+")")
	}
	banner += titleStyle.Render(" ⚡")
	b.WriteString("\n" + banner + "\n\n")

	switch m.mode {
	case ModeNewDir:
		b.WriteString("  " + newSessionStyle.Render("디렉토리 추가") + "\n\n")
		b.WriteString("  " + promptTopStyle.Render("┌─ 디렉토리 경로") + "\n")
		b.WriteString("  " + promptArrowStyle.Render("└─➤ ") + m.textInput.View() + "\n\n")

		// 추천 목록 표시
		if len(m.dirSuggestions) > 0 {
			b.WriteString("  " + modeInactiveStyle.Render("추천:") + "\n")
			for i, suggestion := range m.dirSuggestions {
				displayPath := suggestion
				if len(displayPath) > 50 {
					displayPath = "..." + displayPath[len(displayPath)-47:]
				}
				if i == m.dirSuggestionIdx {
					b.WriteString("  " + completeSelectedStyle.Render("▸ "+displayPath) + "\n")
				} else {
					b.WriteString("  " + dirPathStyle.Render("  "+displayPath) + "\n")
				}
			}
			b.WriteString("\n")
		}

		help := modeInactiveStyle.Render("  [tab] 추천선택  [enter] 추가")
		if len(m.dirs) > 0 {
			help += modeInactiveStyle.Render("  [esc] 취소")
		}
		b.WriteString(help + "\n")

	case ModeNewDirLabel:
		displayPath := m.pendingDirPath
		if len(displayPath) > 40 {
			displayPath = "..." + displayPath[len(displayPath)-37:]
		}
		if m.editingDirIdx >= 0 {
			b.WriteString("  " + newSessionStyle.Render("디렉토리 설명 수정") + "\n\n")
		} else {
			b.WriteString("  " + newSessionStyle.Render("디렉토리 설명 입력") + "\n\n")
		}
		b.WriteString("  " + dirPathStyle.Render("경로: "+displayPath) + "\n\n")
		b.WriteString("  " + promptTopStyle.Render("┌─ 설명 (선택사항)") + "\n")
		b.WriteString("  " + promptArrowStyle.Render("└─➤ ") + m.textInput.View() + "\n\n")
		var help string
		if m.editingDirIdx >= 0 {
			help = modeInactiveStyle.Render("  [enter] 저장  [esc] 취소")
		} else {
			help = modeInactiveStyle.Render("  [enter] 저장  [esc] 설명 없이 저장")
		}
		b.WriteString(help + "\n")

	case ModeConfirmDeleteDir:
		b.WriteString(m.viewDirList())
		b.WriteString("\n")
		dirEntry := m.dirs[m.deleteTarget]
		displayName := dirEntry.Path
		if dirEntry.Label != "" {
			displayName = dirEntry.Label
		} else if len(displayName) > 40 {
			displayName = "..." + displayName[len(displayName)-37:]
		}
		b.WriteString("  " + deletePromptStyle.Render(fmt.Sprintf("'%s' 삭제할까요? (y/n)", displayName)) + "\n")

	case ModeDirList:
		b.WriteString(m.viewDirList())
		b.WriteString("\n")
		help := modeInactiveStyle.Render("  [j/k] 이동  [enter] 추가  [e] 설명수정  [x] 삭제  [esc] 닫기")
		b.WriteString(help + "\n")

	case ModeNewProject:
		b.WriteString("  " + newSessionStyle.Render("새 프로젝트 만들기") + "\n\n")
		b.WriteString("  " + promptTopStyle.Render("┌─ 프로젝트 이름") + "\n")
		b.WriteString("  " + promptArrowStyle.Render("└─➤ ") + m.textInput.View() + "\n\n")
		help := modeInactiveStyle.Render("  [enter] 생성  [esc] 취소")
		b.WriteString(help + "\n")

	case ModeConfirmDeleteProject:
		b.WriteString(m.viewProjectList())
		b.WriteString("\n")
		projectName := m.projects[m.deleteTarget].Name
		b.WriteString("  " + deletePromptStyle.Render(fmt.Sprintf("'%s' 프로젝트를 삭제할까요? (y/n)", projectName)) + "\n")

	case ModeProjectList:
		b.WriteString(m.viewProjectList())
		b.WriteString("\n")
		help := modeInactiveStyle.Render("  [j/k] 이동  [enter] 선택  [r] 이름수정  [x] 삭제")
		b.WriteString(help + "\n")

	case ModeRenameProject:
		b.WriteString("  " + newSessionStyle.Render("프로젝트 이름 수정") + "\n\n")
		b.WriteString("  " + promptTopStyle.Render("┌─ 새 이름") + "\n")
		b.WriteString("  " + promptArrowStyle.Render("└─➤ ") + m.textInput.View() + "\n\n")
		help := modeInactiveStyle.Render("  [enter] 저장  [esc] 취소")
		b.WriteString(help + "\n")

	case ModeNewSubSession:
		b.WriteString(m.viewSessionTabs())
		b.WriteString("  " + newSessionStyle.Render("새 서브세션 만들기") + "\n\n")
		sessionName := ""
		if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
			sessionName = m.sessions[m.currentSessionIdx].Name
		}
		b.WriteString("  " + modeInactiveStyle.Render("세션: "+sessionName) + "\n\n")
		b.WriteString("  " + promptTopStyle.Render("┌─ 목적 (선택사항)") + "\n")
		b.WriteString("  " + promptArrowStyle.Render("└─➤ ") + m.textInput.View() + "\n\n")
		help := modeInactiveStyle.Render("  [enter] 생성  [esc] 취소")
		b.WriteString(help + "\n")

	case ModeEditGoal:
		b.WriteString(m.viewSessionTabs())
		b.WriteString("  " + newSessionStyle.Render("목적 수정") + "\n\n")
		subNum := 0
		if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
			session := m.sessions[m.currentSessionIdx]
			if m.currentSubSessionIdx >= 0 && m.currentSubSessionIdx < len(session.SubSessions) {
				subNum = session.SubSessions[m.currentSubSessionIdx].Number
			}
		}
		b.WriteString("  " + modeInactiveStyle.Render(fmt.Sprintf("서브세션: #%d", subNum)) + "\n\n")
		b.WriteString("  " + promptTopStyle.Render("┌─ 목적") + "\n")
		b.WriteString("  " + promptArrowStyle.Render("└─➤ ") + m.textInput.View() + "\n\n")
		help := modeInactiveStyle.Render("  [enter] 저장  [esc] 취소")
		b.WriteString(help + "\n")

	case ModeConfirmDone:
		b.WriteString(m.viewSessionTabs())
		b.WriteString(m.viewHistory())
		b.WriteString("\n")
		subNum := 0
		if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
			session := m.sessions[m.currentSessionIdx]
			if m.currentSubSessionIdx >= 0 && m.currentSubSessionIdx < len(session.SubSessions) {
				subNum = session.SubSessions[m.currentSubSessionIdx].Number
			}
		}
		b.WriteString("  " + deletePromptStyle.Render(fmt.Sprintf("서브세션 #%d를 완료하고 삭제할까요? (y/n)", subNum)) + "\n")

	case ModeConfirmDelete:
		b.WriteString(m.viewSessionList())
		b.WriteString("\n")
		sessionName := m.sessions[m.deleteTarget].Name
		b.WriteString("  " + deletePromptStyle.Render(fmt.Sprintf("'%s' 세션을 삭제할까요? (y/n)", sessionName)) + "\n")

	case ModeSessionList:
		b.WriteString(m.viewSessionList())
		b.WriteString("\n")
		help := modeInactiveStyle.Render("  [j/k] 이동  [enter] 선택  [r] 이름수정  [e] 설명수정  [x] 삭제")
		b.WriteString(help + "\n")

	case ModeRenameSession:
		b.WriteString("  " + newSessionStyle.Render("세션 이름 수정") + "\n\n")
		b.WriteString("  " + promptTopStyle.Render("┌─ 새 이름") + "\n")
		b.WriteString("  " + promptArrowStyle.Render("└─➤ ") + m.textInput.View() + "\n\n")
		help := modeInactiveStyle.Render("  [enter] 저장  [esc] 취소")
		b.WriteString(help + "\n")

	case ModeNewSession:
		b.WriteString("  " + newSessionStyle.Render("새 세션 만들기") + "\n\n")
		b.WriteString("  " + promptTopStyle.Render("┌─ 세션 이름") + "\n")
		b.WriteString("  " + promptArrowStyle.Render("└─➤ ") + m.textInput.View() + "\n\n")
		help := modeInactiveStyle.Render("  [enter] 다음  [esc] 취소")
		b.WriteString(help + "\n")

	case ModeNewSessionLabel:
		if m.editingSessionIdx >= 0 {
			b.WriteString("  " + newSessionStyle.Render("세션 설명 수정") + "\n\n")
		} else {
			b.WriteString("  " + newSessionStyle.Render("세션 설명 입력") + "\n\n")
		}
		b.WriteString("  " + modeInactiveStyle.Render("이름: "+m.pendingSessionName) + "\n\n")
		b.WriteString("  " + promptTopStyle.Render("┌─ 설명 (선택사항)") + "\n")
		b.WriteString("  " + promptArrowStyle.Render("└─➤ ") + m.textInput.View() + "\n\n")
		var help string
		if m.editingSessionIdx >= 0 {
			help = modeInactiveStyle.Render("  [enter] 저장  [esc] 취소")
		} else {
			help = modeInactiveStyle.Render("  [enter] 저장  [esc] 설명 없이 저장")
		}
		b.WriteString(help + "\n")

	case ModeQueue:
		b.WriteString(m.viewQueue())
		b.WriteString(m.viewModeIndicator())

	case ModeInput:
		b.WriteString(m.viewSessionTabs())
		b.WriteString(m.viewHistory())
		b.WriteString(m.viewModeIndicator())
		sessionName := "session"
		if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
			sessionName = m.sessions[m.currentSessionIdx].Name
		}
		b.WriteString(m.viewPrompt(sessionName))
	}

	return b.String()
}

func (m model) viewDirList() string {
	var b strings.Builder
	b.WriteString("  " + modeActiveStyle.Render("Directories") + " " + modeInactiveStyle.Render(fmt.Sprintf("(%d개)", len(m.dirs))) + "\n\n")

	if len(m.dirs) == 0 {
		b.WriteString("  " + modeInactiveStyle.Render("  (등록된 디렉토리 없음)") + "\n")
	}

	for i, dir := range m.dirs {
		displayPath := dir.Path
		if len(displayPath) > 40 {
			displayPath = "..." + displayPath[len(displayPath)-37:]
		}
		var displayText string
		if dir.Label != "" {
			displayText = dir.Label + " " + dirPathStyle.Render("("+displayPath+")")
		} else {
			displayText = displayPath
		}

		if i == m.listCursor {
			if dir.Label != "" {
				b.WriteString("  " + listSelectedStyle.Render(" ▸ "+dir.Label+" ") + " " + dirPathStyle.Render("("+displayPath+")") + "\n")
			} else {
				b.WriteString("  " + listSelectedStyle.Render(" ▸ "+displayPath+" ") + "\n")
			}
		} else {
			b.WriteString("  " + dirPathStyle.Render("  "+displayText) + "\n")
		}
	}

	if m.listCursor == len(m.dirs) {
		b.WriteString("  " + listSelectedStyle.Render(" + add directory ") + "\n")
	} else {
		b.WriteString("  " + newSessionStyle.Render("  + add directory") + "\n")
	}

	return b.String()
}

func (m model) viewProjectList() string {
	var b strings.Builder
	b.WriteString("  " + modeActiveStyle.Render("Projects") + "\n\n")

	for i, project := range m.projects {
		prefix := "  "
		if i == m.listCursor {
			b.WriteString(prefix + listSelectedStyle.Render(" ▸ "+project.Name+" ") + "\n")
		} else {
			b.WriteString(prefix + listItemStyle.Render("  "+project.Name) + "\n")
		}
	}

	if m.listCursor == len(m.projects) {
		b.WriteString("  " + listSelectedStyle.Render(" + new project ") + "\n")
	} else {
		b.WriteString("  " + newSessionStyle.Render("  + new project") + "\n")
	}

	return b.String()
}

func (m model) viewSessionList() string {
	var b strings.Builder
	b.WriteString("  " + modeActiveStyle.Render("Sessions") + "\n\n")

	for i, session := range m.sessions {
		status := m.getSubSessionStatus(session)
		statusDot := m.getStatusDot(status)
		prefix := "  "
		labelText := ""
		if session.Label != "" {
			labelText = " " + modeInactiveStyle.Render("("+session.Label+")")
		}
		subCountText := fmt.Sprintf(" [%d]", len(session.SubSessions))
		if i == m.listCursor {
			b.WriteString(prefix + listSelectedStyle.Render(" ▸ "+session.Name+" ") + modeInactiveStyle.Render(subCountText) + labelText + " " + statusDot + "\n")
		} else {
			b.WriteString(prefix + listItemStyle.Render("  "+session.Name) + modeInactiveStyle.Render(subCountText) + labelText + " " + statusDot + "\n")
		}
		// 서브세션 표시 (선택된 세션만)
		if i == m.listCursor && len(session.SubSessions) > 0 {
			for _, sub := range session.SubSessions {
				subDot := m.getStatusDot(sub.Status)
				goalText := ""
				if sub.Goal != "" {
					goalText = modeInactiveStyle.Render(" - " + sub.Goal)
					if len(goalText) > 40 {
						goalText = goalText[:40] + "..."
					}
				}
				b.WriteString(prefix + "    " + dirPathStyle.Render(fmt.Sprintf("#%d", sub.Number)) + goalText + " " + subDot + "\n")
			}
		}
	}

	if m.listCursor == len(m.sessions) {
		b.WriteString("  " + listSelectedStyle.Render(" + new session ") + "\n")
	} else {
		b.WriteString("  " + newSessionStyle.Render("  + new session") + "\n")
	}

	return b.String()
}

func (m model) viewQueue() string {
	var b strings.Builder

	taHeight := m.textInput.Height()
	if taHeight < 1 {
		taHeight = 1
	}
	// 화면 높이에서 고정 요소들을 뺌
	historyHeight := m.height - 12 - taHeight
	if historyHeight < 5 {
		historyHeight = 5
	}

	// 활성 큐 항목만 수집
	type queueEntry struct {
		index        int
		item         QueueItem
		sessionLabel string
	}
	var activeItems []queueEntry
	for i, item := range m.queue {
		if item.Status == QueuePending || item.Status == QueueRunning {
			sessionLabel := "?"
			if item.SessionIdx >= 0 && item.SessionIdx < len(m.sessions) {
				session := m.sessions[item.SessionIdx]
				sessionLabel = session.Name
				if item.SubSessionIdx >= 0 && item.SubSessionIdx < len(session.SubSessions) {
					sessionLabel += fmt.Sprintf("#%d", session.SubSessions[item.SubSessionIdx].Number)
				}
			}
			activeItems = append(activeItems, queueEntry{i, item, sessionLabel})
		}
	}

	runningCount := 0
	pendingCount := 0
	for _, e := range activeItems {
		if e.item.Status == QueueRunning {
			runningCount++
		} else {
			pendingCount++
		}
	}

	b.WriteString("  " + modeActiveStyle.Render("Queue") + " " + modeInactiveStyle.Render(fmt.Sprintf("(실행: %d, 대기: %d)", runningCount, pendingCount)) + "\n\n")

	displayed := 2
	if len(activeItems) == 0 {
		b.WriteString("  " + modeInactiveStyle.Render("(비어있음)") + "\n")
		displayed++
	} else {
		for i, entry := range activeItems {
			var statusMark string
			var loadingLine string

			switch entry.item.Status {
			case QueueRunning:
				statusMark = statusRunningStyle.Render("▶")
				dots := strings.Repeat(".", m.thinkingDot+1)
				loadingLine = "      " + thinkingStyle.Render("생각중"+dots)
			case QueuePending:
				statusMark = statusWaitingStyle.Render("◦")
				dots := strings.Repeat(".", m.thinkingDot+1)
				loadingLine = "      " + modeInactiveStyle.Render("대기중"+dots)
			}

			// 프롬프트 표시 (줄바꿈 처리)
			prompt := strings.ReplaceAll(entry.item.Prompt, "\n", " ")
			maxLen := m.width - 20
			if maxLen < 20 {
				maxLen = 20
			}
			if len(prompt) > maxLen {
				prompt = prompt[:maxLen] + "..."
			}

			// 세션/서브세션 라벨
			labelStr := modeInactiveStyle.Render("[" + entry.sessionLabel + "]")

			// 선택된 항목 강조
			if i == m.queueCursor {
				b.WriteString("  " + listSelectedStyle.Render(" ▸ ") + statusMark + " " + labelStr + " " + prompt + "\n")
			} else {
				b.WriteString("    " + statusMark + " " + labelStr + " " + queueItemStyle.Render(prompt) + "\n")
			}
			displayed++

			// 실행 중이면 아래에 로딩 표시
			if loadingLine != "" {
				b.WriteString(loadingLine + "\n")
				displayed++
			}
		}
	}

	// 도움말
	b.WriteString("\n")
	help := modeInactiveStyle.Render("  [j/k] 이동  [enter] 우선실행  [x] 삭제  [esc] 닫기")
	b.WriteString(help + "\n")
	displayed += 2

	for i := displayed; i < historyHeight; i++ {
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) viewHistory() string {
	var b strings.Builder

	taHeight := m.textInput.Height()
	if taHeight < 1 {
		taHeight = 1
	}
	// 화면 높이에서 고정 요소들을 뺌:
	// banner(3줄) + sessionTabs(최대5줄) + modeIndicator(1줄) + prompt헤더(1줄) + prompt여백(1줄) + history여백(1줄) = 12줄
	historyHeight := m.height - 12 - taHeight
	if historyHeight < 3 {
		historyHeight = 3
	}

	var history []string
	if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
		session := m.sessions[m.currentSessionIdx]
		if m.currentSubSessionIdx >= 0 && m.currentSubSessionIdx < len(session.SubSessions) {
			subSession := session.SubSessions[m.currentSubSessionIdx]
			history = subSession.History
		}
	}

	// 스크롤 적용한 시작 인덱스
	startIdx := 0
	endIdx := len(history)
	if len(history) > historyHeight {
		// 기본: 맨 아래 보여줌, 스크롤하면 위로 올라감
		startIdx = len(history) - historyHeight - m.historyScroll
		if startIdx < 0 {
			startIdx = 0
		}
		endIdx = startIdx + historyHeight
		if endIdx > len(history) {
			endIdx = len(history)
		}
	}

	// 패딩을 먼저 출력 (빈 공간이 위에 오도록)
	displayedLines := endIdx - startIdx
	// 큐에 있는 프롬프트 개수만큼 상태 줄 추가
	for i := startIdx; i < endIdx; i++ {
		line := history[i]
		if strings.HasPrefix(line, "→ [") {
			endBracket := strings.Index(line[4:], "]")
			if endBracket > 0 {
				queueID := line[5 : 4+endBracket]
				for _, item := range m.queue {
					if item.ID == queueID {
						displayedLines++
						break
					}
				}
			}
		}
	}
	for i := displayedLines; i < historyHeight; i++ {
		b.WriteString("\n")
	}

	// 히스토리 출력
	dots := strings.Repeat(".", m.thinkingDot)
	for i := startIdx; i < endIdx; i++ {
		line := history[i]
		if strings.HasPrefix(line, "→ ") {
			// queueID 마커 확인: "→ [queueID] 내용" 형식
			displayLine := line
			if len(line) > 4 && line[4] == '[' {
				// queueID 추출
				endBracket := strings.Index(line[4:], "]")
				if endBracket > 0 {
					queueID := line[5 : 4+endBracket]
					content := line[4+endBracket+2:] // "] " 이후
					displayLine = "→ " + content

					// 큐에서 상태 확인
					var itemStatus QueueItemStatus = -1
					for _, item := range m.queue {
						if item.ID == queueID {
							itemStatus = item.Status
							break
						}
					}

					b.WriteString("  " + userMsgStyle.Render(displayLine) + "\n")
					// 상태에 따라 추가 표시
					if itemStatus == QueueRunning {
						b.WriteString("  " + thinkingStyle.Render("생성중"+dots) + "\n")
					} else if itemStatus == QueuePending {
						b.WriteString("  " + modeInactiveStyle.Render("대기중"+dots) + "\n")
					}
					continue
				}
			}
			b.WriteString("  " + userMsgStyle.Render(displayLine) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}

	b.WriteString("\n") // 프롬프트 위 한 줄 여백

	return b.String()
}

func (m model) viewModeIndicator() string {
	// queue에서 대기/실행 중인 항목 수
	queueCount := 0
	for _, item := range m.queue {
		if item.Status == QueuePending || item.Status == QueueRunning {
			queueCount++
		}
	}

	queueLabel := "queue"
	if queueCount > 0 {
		queueLabel = fmt.Sprintf("queue (%d)", queueCount)
	}

	var sessionTab, queueTab string
	if m.mode == ModeInput {
		sessionTab = modeActiveStyle.Render("● session")
		queueTab = modeInactiveStyle.Render("○ " + queueLabel)
	} else {
		sessionTab = modeInactiveStyle.Render("○ session")
		queueTab = modeActiveStyle.Render("● " + queueLabel)
	}
	return fmt.Sprintf("  %s  %s\n", sessionTab, queueTab)
}

// wrapText는 텍스트를 주어진 폭에 맞게 줄바꿈합니다
func wrapText(text string, width int) []string {
	if width < 1 {
		width = 50
	}
	var result []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			result = append(result, "")
			continue
		}
		runes := []rune(line)
		currentLine := ""
		currentWidth := 0
		for _, r := range runes {
			charWidth := lipgloss.Width(string(r))
			if currentWidth+charWidth > width {
				result = append(result, currentLine)
				currentLine = string(r)
				currentWidth = charWidth
			} else {
				currentLine += string(r)
				currentWidth += charWidth
			}
		}
		if currentLine != "" || len(runes) == 0 {
			result = append(result, currentLine)
		}
	}
	if len(result) == 0 {
		result = []string{""}
	}
	return result
}

func (m model) viewPrompt(modeLabel string) string {
	var b strings.Builder

	// 세션 이름 + 서브세션 번호 + provider + 목적 표시
	headerParts := []string{modeLabel}
	var subSessionGoal string
	var providerTag string
	if m.currentSessionIdx >= 0 && m.currentSessionIdx < len(m.sessions) {
		session := m.sessions[m.currentSessionIdx]
		if m.currentSubSessionIdx >= 0 && m.currentSubSessionIdx < len(session.SubSessions) {
			subSession := session.SubSessions[m.currentSubSessionIdx]
			headerParts = append(headerParts, fmt.Sprintf("#%d", subSession.Number))
			subSessionGoal = subSession.Goal
			provider := getAIProvider(&subSession)
			providerTag = modeInactiveStyle.Render("[" + provider + "]")
		}
	}
	header := strings.Join(headerParts, " ")
	if providerTag != "" {
		header += " " + providerTag
	}
	if subSessionGoal != "" {
		goalDisplay := subSessionGoal
		if len(goalDisplay) > 40 {
			goalDisplay = goalDisplay[:40] + "..."
		}
		header += "  " + modeInactiveStyle.Render(goalDisplay)
	}

	b.WriteString("  " + promptTopStyle.Render("┌─ "+header) + "\n")

	// textarea 내용을 직접 줄바꿈해서 렌더링
	content := m.textInput.Value()
	inputWidth := m.textInput.Width()
	if inputWidth < 1 {
		inputWidth = 50
	}

	cursorStyle := lipgloss.NewStyle().Background(lipgloss.Color("252"))
	placeholderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))

	// placeholder 표시 (입력이 비어있고 placeholder가 있을 때)
	if content == "" && m.textInput.Placeholder != "" {
		// 커서 + 회색 placeholder
		b.WriteString("  " + promptArrowStyle.Render("└─➤ ") + cursorStyle.Render(" ") + placeholderStyle.Render(m.textInput.Placeholder))
	} else {
		// 커서 위치 정보 가져오기
		cursorRow := m.textInput.Line()
		lineInfo := m.textInput.LineInfo()
		cursorCol := lineInfo.CharOffset

		lines := strings.Split(content, "\n")
		wrappedLines := wrapText(content, inputWidth)

		// 커서 위치를 wrap된 줄에서 계산
		wrapRow := 0
		for i := 0; i < cursorRow && i < len(lines); i++ {
			lineRunes := []rune(lines[i])
			lineLen := len(lineRunes)
			// 이 줄이 몇 개의 wrap된 줄로 표시되는지
			if lineLen == 0 {
				wrapRow++
			} else {
				wrapRow += (lineLen + inputWidth - 1) / inputWidth
			}
		}
		// 현재 줄 내에서의 wrap 위치 (RowOffset 사용)
		wrapRow += lineInfo.RowOffset
		wrapCol := cursorCol
		if lineInfo.RowOffset > 0 {
			// wrap된 줄에서는 CharOffset이 그 줄 시작부터의 오프셋
			wrapCol = cursorCol - (lineInfo.RowOffset * inputWidth)
			if wrapCol < 0 {
				wrapCol = cursorCol % inputWidth
			}
		}

		// 경계 조건 체크
		if wrapRow < 0 {
			wrapRow = 0
		}
		if wrapCol < 0 {
			wrapCol = 0
		}

		// 커서 삽입
		for i := range wrappedLines {
			if i == wrapRow {
				lineRunes := []rune(wrappedLines[i])
				if wrapCol >= len(lineRunes) {
					// 줄 끝에 커서
					wrappedLines[i] = wrappedLines[i] + cursorStyle.Render(" ")
				} else if wrapCol >= 0 {
					// 줄 중간에 커서
					before := string(lineRunes[:wrapCol])
					cursorChar := string(lineRunes[wrapCol])
					after := string(lineRunes[wrapCol+1:])
					wrappedLines[i] = before + cursorStyle.Render(cursorChar) + after
				}
			}
		}

		// 커서가 마지막 줄 넘어갔을 때 (빈 줄에서)
		if wrapRow >= len(wrappedLines) {
			wrappedLines = append(wrappedLines, cursorStyle.Render(" "))
		}

		for i, line := range wrappedLines {
			if i == 0 {
				b.WriteString("  " + promptArrowStyle.Render("└─➤ ") + line)
			} else {
				b.WriteString("\n       " + line)
			}
		}
	}

	// 명령어 추천 표시
	if len(m.cmdSuggestions) > 0 {
		b.WriteString("\n  " + modeInactiveStyle.Render("추천: "+strings.Join(m.cmdSuggestions, ", ")+" [tab]"))
	}

	b.WriteString("\n") // 아래 한 줄 여백
	return b.String()
}

// buildContextPrompt는 컨텍스트 정보를 포함한 최종 프롬프트를 생성합니다
func buildContextPrompt(prompt string, dirs []DirEntry, sessionLabel string, subSessionGoal string) string {
	var contextParts []string

	if len(dirs) > 0 {
		contextParts = append(contextParts, "[디렉토리]")
		for _, dir := range dirs {
			contextParts = append(contextParts, dir.Path)
			if dir.Label != "" {
				contextParts = append(contextParts, dir.Label)
			}
			contextParts = append(contextParts, "")
		}
	}

	if sessionLabel != "" {
		contextParts = append(contextParts, "[세션 설명]")
		contextParts = append(contextParts, sessionLabel)
	}

	if subSessionGoal != "" {
		contextParts = append(contextParts, "[현재 작업 목적]")
		contextParts = append(contextParts, subSessionGoal)
	}

	if len(contextParts) > 0 {
		return strings.Join(contextParts, "\n") + "\n\n" + prompt
	}
	return prompt
}

func runGemini(prompt string, isFirst bool, sessionIdx int, subSessionIdx int, queueID string, dirs []DirEntry, sessionLabel string, subSessionGoal string) tea.Cmd {
	return func() tea.Msg {
		finalPrompt := buildContextPrompt(prompt, dirs, sessionLabel, subSessionGoal)
		escaped := "'" + strings.ReplaceAll(finalPrompt, "'", "'\\''") + "'"

		var cmdStr string
		if isFirst {
			cmdStr = fmt.Sprintf("gemini -p %s -y", escaped)
		} else {
			cmdStr = fmt.Sprintf("gemini -p %s --resume latest -y", escaped)
		}

		for _, dir := range dirs {
			escapedDir := "'" + strings.ReplaceAll(dir.Path, "'", "'\\''") + "'"
			cmdStr += fmt.Sprintf(" --include-directories %s", escapedDir)
		}

		cmd := exec.Command("sh", "-c", cmdStr)
		if len(dirs) > 0 {
			cmd.Dir = dirs[0].Path
		} else {
			home, _ := os.UserHomeDir()
			cmd.Dir = home
		}

		runningProcessMutex.Lock()
		runningProcess = cmd
		runningProcessMutex.Unlock()

		output, err := cmd.Output()

		runningProcessMutex.Lock()
		runningProcess = nil
		runningProcessMutex.Unlock()

		response := strings.TrimSpace(string(output))
		if err != nil && response == "" {
			response = "Error: " + err.Error()
		}

		return claudeResponseMsg{
			sessionIdx:    sessionIdx,
			subSessionIdx: subSessionIdx,
			queueID:       queueID,
			response:      response,
		}
	}
}

func runClaude(prompt string, subSessionID string, isFirst bool, sessionIdx int, subSessionIdx int, queueID string, dirs []DirEntry, sessionLabel string, subSessionGoal string) tea.Cmd {
	return func() tea.Msg {
		finalPrompt := buildContextPrompt(prompt, dirs, sessionLabel, subSessionGoal)
		escaped := "'" + strings.ReplaceAll(finalPrompt, "'", "'\\''") + "'"

		var cmdStr string
		if isFirst {
			cmdStr = fmt.Sprintf("claude -p %s --session-id %s --dangerously-skip-permissions", escaped, subSessionID)
		} else {
			cmdStr = fmt.Sprintf("claude -p %s --resume %s --dangerously-skip-permissions", escaped, subSessionID)
		}

		for _, dir := range dirs {
			escapedDir := "'" + strings.ReplaceAll(dir.Path, "'", "'\\''") + "'"
			cmdStr += fmt.Sprintf(" --add-dir %s", escapedDir)
		}

		cmd := exec.Command("sh", "-c", cmdStr)
		if len(dirs) > 0 {
			cmd.Dir = dirs[0].Path
		} else {
			home, _ := os.UserHomeDir()
			cmd.Dir = home
		}

		var stderrBuf strings.Builder
		cmd.Stderr = &stderrBuf

		runningProcessMutex.Lock()
		runningProcess = cmd
		runningProcessMutex.Unlock()

		output, err := cmd.Output()

		runningProcessMutex.Lock()
		runningProcess = nil
		runningProcessMutex.Unlock()

		// "No conversation found with session ID" 에러 시 새 세션으로 재시도
		if err != nil && strings.Contains(stderrBuf.String(), "No conversation found with session ID") {
			cmdStr = fmt.Sprintf("claude -p %s --session-id %s --dangerously-skip-permissions", escaped, subSessionID)
			for _, dir := range dirs {
				escapedDir := "'" + strings.ReplaceAll(dir.Path, "'", "'\\''") + "'"
				cmdStr += fmt.Sprintf(" --add-dir %s", escapedDir)
			}

			cmd = exec.Command("sh", "-c", cmdStr)
			if len(dirs) > 0 {
				cmd.Dir = dirs[0].Path
			} else {
				home, _ := os.UserHomeDir()
				cmd.Dir = home
			}

			runningProcessMutex.Lock()
			runningProcess = cmd
			runningProcessMutex.Unlock()

			output, err = cmd.Output()

			runningProcessMutex.Lock()
			runningProcess = nil
			runningProcessMutex.Unlock()
		}

		response := strings.TrimSpace(string(output))
		if err != nil && response == "" {
			response = "Error: " + err.Error()
		}

		return claudeResponseMsg{
			sessionIdx:    sessionIdx,
			subSessionIdx: subSessionIdx,
			queueID:       queueID,
			response:      response,
		}
	}
}

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
		os.Exit(1)
	}
}

package haomafiledialog

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type FS interface {
	ReadDir(path string) ([]Entry, error)
}

type Entry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

type OSFS struct{}

func (OSFS) ReadDir(path string) ([]Entry, error) {
	de, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(de))
	for _, d := range de {
		info, err := d.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Name:    d.Name(),
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	return out, nil
}

type Model struct {
	fs         FS
	currentDir string
	filter     string
	showHidden bool

	entries []Entry
	err     error
}

func NewModel(filesys FS, startDir string) *Model {
	if filesys == nil {
		filesys = OSFS{}
	}
	m := &Model{fs: filesys, currentDir: filepath.Clean(startDir)}
	m.refresh()
	return m
}

func (m *Model) CurrentDir() string { return m.currentDir }

func (m *Model) Entries() []Entry { return m.entries }

func (m *Model) Err() error { return m.err }

func (m *Model) Filter() string { return m.filter }

func (m *Model) ShowHidden() bool { return m.showHidden }

func (m *Model) SetFilter(s string) {
	m.filter = s
	m.refresh()
}

func (m *Model) SetShowHidden(b bool) {
	m.showHidden = b
	m.refresh()
}

func (m *Model) NavigateInto(name string) {
	if name == ".." {
		m.NavigateUp()
		return
	}
	for _, e := range m.entries {
		if e.Name == name && e.IsDir {
			m.currentDir = filepath.Join(m.currentDir, name)
			m.refresh()
			return
		}
	}
}

func (m *Model) NavigateUp() {
	parent := filepath.Dir(m.currentDir)
	if parent == m.currentDir {
		return
	}
	m.currentDir = parent
	m.refresh()
}

func (m *Model) AtRoot() bool {
	return filepath.Dir(m.currentDir) == m.currentDir
}

func (m *Model) ResolvePath(name string) string {
	if name == "" || name == "." {
		return m.currentDir
	}
	if name == ".." {
		return filepath.Dir(m.currentDir)
	}
	return filepath.Join(m.currentDir, name)
}

func (m *Model) IsDir(name string) bool {
	if name == ".." || name == "." {
		return true
	}
	for _, e := range m.entries {
		if e.Name == name {
			return e.IsDir
		}
	}
	return false
}

func (m *Model) refresh() {
	raw, err := m.fs.ReadDir(m.currentDir)
	if err != nil {
		m.err = err
		m.entries = nil
		if !m.AtRoot() {
			m.entries = []Entry{{Name: "..", IsDir: true}}
		}
		return
	}
	m.err = nil

	needle := strings.ToLower(m.filter)
	filtered := make([]Entry, 0, len(raw))
	for _, e := range raw {
		if !m.showHidden && strings.HasPrefix(e.Name, ".") {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(e.Name), needle) {
			continue
		}
		filtered = append(filtered, e)
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].IsDir != filtered[j].IsDir {
			return filtered[i].IsDir
		}
		return strings.ToLower(filtered[i].Name) < strings.ToLower(filtered[j].Name)
	})

	if !m.AtRoot() {
		filtered = append([]Entry{{Name: "..", IsDir: true}}, filtered...)
	}
	m.entries = filtered
}

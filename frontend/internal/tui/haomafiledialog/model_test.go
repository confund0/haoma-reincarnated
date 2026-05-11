package haomafiledialog

import (
	"errors"
	"io/fs"
	"testing"
	"time"
)

type fakeFS struct {
	dirs map[string][]Entry
}

func (f *fakeFS) ReadDir(path string) ([]Entry, error) {
	e, ok := f.dirs[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	cp := make([]Entry, len(e))
	copy(cp, e)
	return cp, nil
}

func newFixture() *fakeFS {
	mt := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	return &fakeFS{dirs: map[string][]Entry{
		"/": {
			{Name: "home", IsDir: true, ModTime: mt},
		},
		"/home": {
			{Name: "alice", IsDir: true, ModTime: mt},
		},
		"/home/alice": {
			{Name: "Downloads", IsDir: true, ModTime: mt},
			{Name: "Documents", IsDir: true, ModTime: mt},
		},
		"/home/alice/Downloads": {
			{Name: "photo.jpg", Size: 1024, ModTime: mt},
			{Name: ".secret_dotfile", Size: 16, ModTime: mt},
			{Name: "notes.txt", Size: 200, ModTime: mt},
			{Name: "work", IsDir: true, ModTime: mt},
		},
		"/home/alice/Documents": {
			{Name: "cv.pdf", Size: 8192, ModTime: mt},
		},
		"/home/alice/Downloads/work": {},
	}}
}

func names(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}

func equalSlices(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("slice len: got %v, want %v", got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("slice[%d]: got %q, want %q (full got=%v want=%v)", i, got[i], want[i], got, want)
			return
		}
	}
}

func TestNew_LoadsStartDir(t *testing.T) {
	m := NewModel(newFixture(), "/home/alice/Downloads")
	if m.Err() != nil {
		t.Fatalf("Err = %v, want nil", m.Err())
	}
	if m.CurrentDir() != "/home/alice/Downloads" {
		t.Errorf("CurrentDir = %q, want /home/alice/Downloads", m.CurrentDir())
	}

	equalSlices(t, names(m.Entries()), []string{"..", "work", "notes.txt", "photo.jpg"})
}

func TestSetFilter_NarrowsResults(t *testing.T) {
	m := NewModel(newFixture(), "/home/alice/Downloads")
	m.SetFilter("oTo")
	equalSlices(t, names(m.Entries()), []string{"..", "photo.jpg"})

	m.SetFilter("secret")
	equalSlices(t, names(m.Entries()), []string{".."})

	m.SetFilter("")
	equalSlices(t, names(m.Entries()), []string{"..", "work", "notes.txt", "photo.jpg"})
}

func TestSetShowHidden_RevealsDotfiles(t *testing.T) {
	m := NewModel(newFixture(), "/home/alice/Downloads")
	m.SetShowHidden(true)
	equalSlices(t, names(m.Entries()), []string{"..", "work", ".secret_dotfile", "notes.txt", "photo.jpg"})

	m.SetShowHidden(false)
	equalSlices(t, names(m.Entries()), []string{"..", "work", "notes.txt", "photo.jpg"})
}

func TestNavigateInto_DescendsIntoDir(t *testing.T) {
	m := NewModel(newFixture(), "/home/alice")
	m.SetFilter("Down")
	if got := m.CurrentDir(); got != "/home/alice" {
		t.Fatalf("pre-nav CurrentDir = %q", got)
	}

	m.NavigateInto("Downloads")
	if got := m.CurrentDir(); got != "/home/alice/Downloads" {
		t.Errorf("post-nav CurrentDir = %q, want /home/alice/Downloads", got)
	}

	equalSlices(t, names(m.Entries()), []string{".."})
}

func TestNavigateInto_FileIsNoOp(t *testing.T) {
	m := NewModel(newFixture(), "/home/alice/Downloads")
	m.NavigateInto("notes.txt")
	if got := m.CurrentDir(); got != "/home/alice/Downloads" {
		t.Errorf("CurrentDir = %q, want unchanged", got)
	}
}

func TestNavigateUp_ReturnsToParent(t *testing.T) {
	m := NewModel(newFixture(), "/home/alice/Downloads")

	m.NavigateUp()
	if got := m.CurrentDir(); got != "/home/alice" {
		t.Errorf("after NavigateUp: CurrentDir = %q, want /home/alice", got)
	}

	m.NavigateInto("..")
	if got := m.CurrentDir(); got != "/home" {
		t.Errorf("after NavigateInto(..): CurrentDir = %q, want /home", got)
	}
}

func TestNavigateUp_AtRootIsNoOp(t *testing.T) {
	m := NewModel(newFixture(), "/")
	if !m.AtRoot() {
		t.Fatalf("AtRoot = false, want true at /")
	}
	m.NavigateUp()
	if got := m.CurrentDir(); got != "/" {
		t.Errorf("after NavigateUp at root: CurrentDir = %q, want /", got)
	}

	if got := names(m.Entries()); len(got) == 0 || got[0] == ".." {
		t.Errorf("at root entries = %v, expected no .. prefix", got)
	}
}

func TestSorting_DirsBeforeFilesAlpha(t *testing.T) {
	mt := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	fs := &fakeFS{dirs: map[string][]Entry{
		"/x": {
			{Name: "Z_dir", IsDir: true, ModTime: mt},
			{Name: "alpha.txt", ModTime: mt},
			{Name: "a_dir", IsDir: true, ModTime: mt},
			{Name: "Beta.md", ModTime: mt},
		},
	}}
	m := NewModel(fs, "/x")

	equalSlices(t, names(m.Entries()), []string{"..", "a_dir", "Z_dir", "alpha.txt", "Beta.md"})
}

func TestErr_LatchedOnReadFailure(t *testing.T) {
	m := NewModel(&fakeFS{dirs: map[string][]Entry{}}, "/home/alice/Downloads")
	if !errors.Is(m.Err(), fs.ErrNotExist) {
		t.Errorf("Err = %v, want fs.ErrNotExist", m.Err())
	}
	if got := names(m.Entries()); len(got) != 1 || got[0] != ".." {
		t.Errorf("entries on read-fail = %v, want [..]", got)
	}
}

func TestResolvePath_VariousInputs(t *testing.T) {
	m := NewModel(newFixture(), "/home/alice/Downloads")
	cases := map[string]string{
		"":          "/home/alice/Downloads",
		".":         "/home/alice/Downloads",
		"..":        "/home/alice",
		"photo.jpg": "/home/alice/Downloads/photo.jpg",
	}
	for in, want := range cases {
		if got := m.ResolvePath(in); got != want {
			t.Errorf("ResolvePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsDir_CoversSpecialCases(t *testing.T) {
	m := NewModel(newFixture(), "/home/alice/Downloads")
	if !m.IsDir("..") {
		t.Errorf("IsDir(..) = false, want true")
	}
	if !m.IsDir("work") {
		t.Errorf("IsDir(work) = false, want true")
	}
	if m.IsDir("photo.jpg") {
		t.Errorf("IsDir(photo.jpg) = true, want false")
	}
	if m.IsDir("nonexistent") {
		t.Errorf("IsDir(nonexistent) = true, want false")
	}
}

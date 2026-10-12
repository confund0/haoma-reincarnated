package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
)

type logMark struct {
	path   string
	info   os.FileInfo
	offset int64
	head   []byte
}

const markHeadLen = 64

func markLog(path string) logMark {
	m := logMark{path: path}
	if st, err := os.Stat(path); err == nil {
		m.info, m.offset = st, st.Size()
		m.head = readHead(path)
	}
	return m
}

func readHead(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, markHeadLen)
	n, _ := io.ReadFull(f, buf)
	return buf[:n]
}

func (m logMark) isMarked(path string, st os.FileInfo) bool {
	if m.info == nil || !os.SameFile(st, m.info) || st.Size() < m.offset {
		return false
	}

	h := readHead(path)
	return len(h) >= len(m.head) && bytes.Equal(h[:len(m.head)], m.head)
}

func haomaLogMark(r *rig) logMark { return markLog(filepath.Join(r.root, "haoma.log")) }

func (m logMark) lines() ([]string, error) {
	cur, err := os.Stat(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if m.isMarked(m.path, cur) {
		return readLinesFrom(m.path, m.offset)
	}
	var out []string
	prev := m.path + ".prev"
	if m.info != nil {
		if pst, err := os.Stat(prev); err == nil && m.isMarked(prev, pst) {
			tail, err := readLinesFrom(prev, m.offset)
			if err != nil {
				return nil, err
			}
			out = tail
		}
	}
	rest, err := readLinesFrom(m.path, 0)
	if err != nil {
		return nil, err
	}
	return append(out, rest...), nil
}

func readLinesFrom(path string, offset int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

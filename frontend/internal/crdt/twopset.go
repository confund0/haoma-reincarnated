package crdt

import "sort"

type TwoPSet struct {
	Added   map[string]int64 `json:"added,omitempty"`
	Removed map[string]int64 `json:"removed,omitempty"`
}

func NewTwoPSet() TwoPSet {
	return TwoPSet{
		Added:   map[string]int64{},
		Removed: map[string]int64{},
	}
}

func (s *TwoPSet) Add(elem string, ts int64) {
	if s.Added == nil {
		s.Added = map[string]int64{}
	}
	if ts > s.Added[elem] {
		s.Added[elem] = ts
	} else if _, ok := s.Added[elem]; !ok {
		s.Added[elem] = ts
	}
}

func (s *TwoPSet) Remove(elem string, ts int64) {
	if s.Removed == nil {
		s.Removed = map[string]int64{}
	}
	if ts > s.Removed[elem] {
		s.Removed[elem] = ts
	} else if _, ok := s.Removed[elem]; !ok {
		s.Removed[elem] = ts
	}
}

func (s TwoPSet) Contains(elem string) bool {
	if _, removed := s.Removed[elem]; removed {
		return false
	}
	_, added := s.Added[elem]
	return added
}

func (s TwoPSet) Members() []string {
	out := make([]string, 0, len(s.Added))
	for elem := range s.Added {
		if _, removed := s.Removed[elem]; removed {
			continue
		}
		out = append(out, elem)
	}
	sort.Strings(out)
	return out
}

func (s *TwoPSet) Merge(other TwoPSet) {
	if s.Added == nil {
		s.Added = map[string]int64{}
	}
	if s.Removed == nil {
		s.Removed = map[string]int64{}
	}
	for elem, ts := range other.Added {
		if ts > s.Added[elem] {
			s.Added[elem] = ts
		} else if _, ok := s.Added[elem]; !ok {
			s.Added[elem] = ts
		}
	}
	for elem, ts := range other.Removed {
		if ts > s.Removed[elem] {
			s.Removed[elem] = ts
		} else if _, ok := s.Removed[elem]; !ok {
			s.Removed[elem] = ts
		}
	}
}

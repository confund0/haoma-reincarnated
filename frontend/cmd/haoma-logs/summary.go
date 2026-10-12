package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

type summaryResult struct {
	query    string
	msgIDs   *idSet
	envIDs   *idSet
	tokens   *idSet
	lines    []entry
	accounts []string
}

func collectSummary(srcs []source, query string, f filters) (*summaryResult, error) {
	r := &summaryResult{
		query:  query,
		msgIDs: newIDSet(),
		envIDs: newIDSet(),
		tokens: newIDSet(),
	}
	r.msgIDs.add(query)

	err := eachEntry(srcs, func(e entry) bool {
		if !matchesAny(e, msgIDKeys, query) {
			return true
		}
		for _, v := range values(e, msgIDKeys) {
			r.msgIDs.add(v)
		}
		for _, v := range values(e, envelopeIDKeys) {
			r.envIDs.add(v)
		}
		for _, v := range values(e, tokenKeys) {
			if looksLikeToken(v) {
				r.tokens.add(v)
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}

	seenAcct := map[string]bool{}
	err = eachEntry(srcs, func(e entry) bool {
		if !f.match(e) {
			return true
		}
		if !r.touches(e) {
			return true
		}
		r.lines = append(r.lines, e)
		if !seenAcct[e.account] {
			seenAcct[e.account] = true
			r.accounts = append(r.accounts, e.account)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(r.accounts)
	return r, nil
}

func (r *summaryResult) touches(e entry) bool {
	for _, a := range e.attrs {
		switch {
		case has(msgIDKeys, a.key) && r.msgIDs.has(a.val):
			return true
		case has(envelopeIDKeys, a.key) && r.envIDs.has(a.val):
			return true
		case has(tokenKeys, a.key) && r.tokens.has(a.val):
			return true
		}
	}
	return false
}

func looksLikeToken(v string) bool {
	return v != "" && !strings.ContainsAny(v, "/\\ ")
}

func (r *summaryResult) write(w io.Writer, m *identityMap) {
	fmt.Fprintf(w, "Message %s — %d lines across %d accounts\n\n",
		short(r.query), len(r.lines), len(r.accounts))
	if len(r.lines) == 0 {
		fmt.Fprintln(w, "  no line in these logs names this message id")
		fmt.Fprintln(w, "  the run may have rotated its logs — try again with -rotated")
		return
	}

	r.writeDevices(w, m)
	r.writeTokens(w)
	r.writeEnvelopes(w)
	r.writeAnswers(w, m)
	r.writeOutcome(w)

	fmt.Fprintln(w, "Timeline")
	for _, e := range r.lines {
		fmt.Fprintln(w, timelineLine(e))
	}
}

type deviceUse struct {
	account string
	peerID  string
	first   entry
	count   int
}

func (r *summaryResult) writeDevices(w io.Writer, m *identityMap) {
	uses := map[string]*deviceUse{}
	var order []string
	for _, e := range r.lines {
		for _, a := range e.attrs {
			if !has(peerIDKeys, a.key) || a.val == "" {
				continue
			}
			k := e.account + "|" + canonicalID(a.val)
			u, ok := uses[k]
			if !ok {
				u = &deviceUse{account: e.account, peerID: a.val, first: e}
				uses[k] = u
				order = append(order, k)
			}
			u.count++
		}
	}
	fmt.Fprintln(w, "Devices named on these lines")
	if len(order) == 0 {
		fmt.Fprintln(w, "  no peer id on any of these lines")
		fmt.Fprintln(w)
		return
	}
	sort.Strings(order)
	for _, k := range order {
		u := uses[k]
		fmt.Fprintf(w, "  %-14s %-12s %-46s first at %s: %s\n",
			u.account, short(u.peerID), m.describe(u.account, u.peerID),
			stamp(u.first), u.first.msg)
	}
	fmt.Fprintln(w)
}

func (r *summaryResult) writeTokens(w io.Writer) {
	fmt.Fprintln(w, "Tokens")
	if r.tokens.len() == 0 {
		fmt.Fprintln(w, "  none — this message carried no file transfer")
		fmt.Fprintln(w)
		return
	}
	type firstSeen struct {
		token   string
		account string
		e       entry
	}
	first := map[string]firstSeen{}
	var order []string

	for _, e := range r.lines {
		for _, a := range e.attrs {
			if !has(tokenKeys, a.key) || !looksLikeToken(a.val) {
				continue
			}
			c := canonicalID(a.val)
			if _, ok := first[c]; ok {
				continue
			}
			first[c] = firstSeen{token: a.val, account: e.account, e: e}
			order = append(order, c)
		}
	}
	for _, c := range order {
		f := first[c]
		fmt.Fprintf(w, "  %-12s first written by %-14s at %s: %s\n",
			short(f.token), f.account, stamp(f.e), f.e.msg)
	}
	fmt.Fprintln(w)
}

func (r *summaryResult) writeEnvelopes(w io.Writer) {
	type envUse struct {
		id       string
		kinds    map[string]bool
		accounts map[string]bool
		first    entry
	}
	uses := map[string]*envUse{}
	var order []string
	for _, e := range r.lines {
		for _, a := range e.attrs {

			if !has(envelopeIDKeys, a.key) || a.val == "" {
				continue
			}
			c := canonicalID(a.val)
			u, ok := uses[c]
			if !ok {
				u = &envUse{id: a.val, kinds: map[string]bool{}, accounts: map[string]bool{}, first: e}
				uses[c] = u
				order = append(order, c)
			}
			u.accounts[e.account] = true
			for _, k := range values(e, kindKeys) {
				if k != "" {
					u.kinds[k] = true
				}
			}
		}
	}
	fmt.Fprintln(w, "Envelopes")
	if len(order) == 0 {
		fmt.Fprintln(w, "  none")
		fmt.Fprintln(w)
		return
	}
	for _, c := range order {
		u := uses[c]
		kinds := strings.Join(sortedKeys(u.kinds), ",")
		if kinds == "" {
			kinds = "not named on these lines"
		}
		fmt.Fprintf(w, "  %-12s kind %-24s seen by %s\n",
			short(u.id), kinds, strings.Join(sortedKeys(u.accounts), ", "))
	}
	fmt.Fprintln(w)
}

func (r *summaryResult) writeAnswers(w io.Writer, m *identityMap) {
	fmt.Fprintln(w, "Answers back")
	n := 0
	for _, e := range r.lines {
		hit := false
		for _, a := range e.attrs {
			if has(ackKeys, a.key) && a.val != "" {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		n++
		from := ""
		for _, a := range e.attrs {
			if has(peerIDKeys, a.key) && a.val != "" {
				from = short(a.val) + " = " + m.describe(e.account, a.val)
				break
			}
		}
		fmt.Fprintf(w, "  %s %-14s %-46s %s\n", stamp(e), e.account, e.msg, from)
	}
	if n == 0 {
		fmt.Fprintln(w, "  no line carries an acknowledgement or a receipt")
	}
	fmt.Fprintln(w)
}

func (r *summaryResult) writeOutcome(w io.Writer) {
	last := map[string]entry{}
	for _, e := range r.lines {
		if len(values(e, stateKeys)) == 0 {
			continue
		}
		last[e.account] = e
	}
	fmt.Fprintln(w, "How it ended on each device")
	if len(last) == 0 {
		fmt.Fprintln(w, "  no line says how it stands")
		fmt.Fprintln(w)
		return
	}
	accts := make([]string, 0, len(last))
	for a := range last {
		accts = append(accts, a)
	}
	sort.Strings(accts)
	for _, a := range accts {
		e := last[a]
		var parts []string
		for _, at := range e.attrs {
			if has(stateKeys, at.key) && at.val != "" {
				parts = append(parts, at.key+"="+at.val)
			}
		}
		fmt.Fprintf(w, "  %-14s %s  %-46s %s\n", a, stamp(e), e.msg, strings.Join(parts, " "))
	}
	fmt.Fprintln(w)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func stamp(e entry) string {
	if !e.hasTS && e.ts.IsZero() {
		return strings.Repeat(" ", len(stampLayout))
	}
	return e.ts.Format(stampLayout)
}

const stampLayout = "2006-01-02T15:04:05.000"

func timelineLine(e entry) string {
	return fmt.Sprintf("%-23s %-20s %s", stamp(e), e.account+"/"+e.process, trimTime(e.raw))
}

func trimTime(line string) string {
	if strings.HasPrefix(line, "time=") {
		if sp := strings.IndexByte(line, ' '); sp > 0 {
			return line[sp+1:]
		}
	}
	return line
}

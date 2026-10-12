package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var kebabName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

func TestSiblingGroupsTable(t *testing.T) {
	if len(siblingGroups) == 0 {
		t.Fatal("siblingGroups is empty")
	}
	pos := map[string]int{}
	for i, g := range siblingGroups {
		if !kebabName.MatchString(g.name) {
			t.Errorf("group %d: name %q is not a short kebab name", i, g.name)
		}
		if g.name == siblingCasesList || g.name == siblingCasesHelp {
			t.Errorf("group %d: %q is a word -cases reads as a request for the list", i, g.name)
		}
		if prev, dup := pos[g.name]; dup {
			t.Errorf("group %q is in the table twice (at %d and %d)", g.name, prev, i)
		}
		pos[g.name] = i
		if g.run == nil {
			t.Errorf("group %q runs nothing", g.name)
		}
	}
	for i, g := range siblingGroups {
		for _, n := range g.needs {
			j, ok := pos[n.group]
			switch {
			case !ok:
				t.Errorf("group %q needs %q, which is not a group", g.name, n.group)
			case j >= i:
				t.Errorf("group %q (at %d) needs %q (at %d): a need must come earlier in the table", g.name, i, n.group, j)
			}
			if strings.TrimSpace(n.why) == "" {
				t.Errorf("group %q needs %q with no reason", g.name, n.group)
			}
		}
	}

	afterDestroyPIN := map[string]bool{"contact-delete": true}
	seen := false
	for _, g := range siblingGroups {
		if seen && !afterDestroyPIN[g.name] {
			t.Errorf("group %q runs after destroy-pin, and it is not one that verifies nothing against dev-a", g.name)
		}
		if g.name == "destroy-pin" {
			seen = true
		}
	}
	if !seen {
		t.Error("the table holds no destroy-pin group")
	}
}

func planNames(gs []siblingGroup) []string {
	if len(gs) == 0 {
		return nil
	}
	return siblingGroupNames(gs)
}

var chainGroups = []siblingGroup{
	{name: "a"},
	{name: "b", needs: []siblingNeed{{group: "a", why: "b needs a"}}},
	{name: "c"},
	{name: "d", needs: []siblingNeed{{group: "b", why: "d needs b"}}},
	{name: "e"},
}

func TestResolveSiblingCases(t *testing.T) {
	all := siblingGroupNames(siblingGroups)
	for _, tc := range []struct {
		name      string
		groups    []siblingGroup
		spec      string
		wantRun   []string
		wantAsked []string
		wantAdded []string
	}{
		{name: "empty is the full run", groups: siblingGroups, spec: "", wantRun: all},
		{name: "blank is the full run", groups: siblingGroups, spec: "  \t ", wantRun: all},
		{name: "one group with no needs", groups: siblingGroups, spec: "text",
			wantRun: []string{"text"}, wantAsked: []string{"text"}},
		{name: "a need is added", groups: siblingGroups, spec: "gapfill",
			wantRun: []string{"peer-fanout", "gapfill"}, wantAsked: []string{"gapfill"}, wantAdded: []string{"peer-fanout"}},
		{name: "a need asked for is not called added", groups: siblingGroups, spec: "gapfill,peer-fanout",
			wantRun: []string{"peer-fanout", "gapfill"}, wantAsked: []string{"peer-fanout", "gapfill"}},
		{name: "the order is the table's, never the names'", groups: siblingGroups, spec: "destroy-pin,trigger,text,file-fanout",
			wantRun:   []string{"text", "peer-fanout", "file-fanout", "trigger", "destroy-pin"},
			wantAsked: []string{"text", "file-fanout", "trigger", "destroy-pin"},
			wantAdded: []string{"peer-fanout"}},
		{name: "a name given twice counts once", groups: siblingGroups, spec: "text,text, text",
			wantRun: []string{"text"}, wantAsked: []string{"text"}},
		{name: "spaces and empty items are dropped", groups: siblingGroups, spec: " clock , ,alias,",
			wantRun: []string{"alias", "clock"}, wantAsked: []string{"alias", "clock"}},
		{name: "a need of a need is added", groups: chainGroups, spec: "d",
			wantRun: []string{"a", "b", "d"}, wantAsked: []string{"d"}, wantAdded: []string{"a", "b"}},
		{name: "a shared need is added once", groups: chainGroups, spec: "e,d,b",
			wantRun: []string{"a", "b", "d", "e"}, wantAsked: []string{"b", "d", "e"}, wantAdded: []string{"a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := resolveSiblingCases(tc.groups, tc.spec, false)
			if err != nil {
				t.Fatalf("resolve %q: %v", tc.spec, err)
			}
			if got := planNames(plan.run); !reflect.DeepEqual(got, tc.wantRun) {
				t.Errorf("run = %v, want %v", got, tc.wantRun)
			}
			if !reflect.DeepEqual(plan.asked, tc.wantAsked) {
				t.Errorf("asked = %v, want %v", plan.asked, tc.wantAsked)
			}
			if !reflect.DeepEqual(plan.added, tc.wantAdded) {
				t.Errorf("added = %v, want %v", plan.added, tc.wantAdded)
			}
			if plan.total != len(tc.groups) {
				t.Errorf("total = %d, want %d", plan.total, len(tc.groups))
			}
			if wantFiltered := len(tc.wantRun) < len(tc.groups); plan.filtered() != wantFiltered {
				t.Errorf("filtered = %v, want %v", plan.filtered(), wantFiltered)
			}
		})
	}
}

func TestResolveSiblingCasesEveryNameIsTheFullRun(t *testing.T) {
	names := siblingGroupNames(siblingGroups)

	rev := make([]string, len(names))
	for i, n := range names {
		rev[len(names)-1-i] = n
	}
	plan, err := resolveSiblingCases(siblingGroups, strings.Join(rev, ","), false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.filtered() {
		t.Fatalf("every group named reads as filtered: %s", plan.describe())
	}
	if got := planNames(plan.run); !reflect.DeepEqual(got, names) {
		t.Fatalf("run = %v, want the table order %v", got, names)
	}
}

func TestResolveSiblingCasesRefuses(t *testing.T) {
	for _, tc := range []struct {
		spec    string
		wantIn  []string
		comment string
	}{
		{spec: "txet", wantIn: []string{`"txet"`}, comment: "a typo"},
		{spec: "text,bogus,gapfill,nope", wantIn: []string{`"bogus"`, `"nope"`}, comment: "every unknown name is named"},
		{spec: "Text", wantIn: []string{`"Text"`}, comment: "names are exact"},
		{spec: "list,text", wantIn: []string{`"list"`}, comment: "the list word is not a group"},
		{spec: " , ,", wantIn: []string{"names no group"}, comment: "commas alone ask for nothing"},
	} {
		t.Run(tc.comment, func(t *testing.T) {
			plan, err := resolveSiblingCases(siblingGroups, tc.spec, false)
			if err == nil {
				t.Fatalf("resolve %q ran %v, want an error", tc.spec, planNames(plan.run))
			}
			msg := err.Error()
			for _, w := range tc.wantIn {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q does not say %q", msg, w)
				}
			}

			for _, n := range siblingGroupNames(siblingGroups) {
				if !strings.Contains(msg, n) {
					t.Errorf("error does not list the valid group %q: %s", n, msg)
				}
			}
		})
	}
}

func TestResolveSiblingCasesBrokenNeed(t *testing.T) {
	groups := []siblingGroup{{name: "a", needs: []siblingNeed{{group: "gone", why: "x"}}}}
	if _, err := resolveSiblingCases(groups, "a", false); err == nil || !strings.Contains(err.Error(), `"gone"`) {
		t.Fatalf("err = %v, want one naming the missing group", err)
	}
}

func TestResolveRunCases(t *testing.T) {
	cfg := config{scenario: scenarioStar, cases: "text"}
	if err := resolveRunCases(&cfg); err == nil || !strings.Contains(err.Error(), "only for -scenario sibling") {
		t.Fatalf("-cases with star: err = %v", err)
	}
	cfg = config{scenario: scenarioStar}
	if err := resolveRunCases(&cfg); err != nil {
		t.Fatalf("star with no -cases: %v", err)
	}
	cfg = config{scenario: scenarioSibling}
	if err := resolveRunCases(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.siblingPlan.filtered() || len(cfg.siblingPlan.run) != len(siblingGroups) {
		t.Fatalf("sibling with no -cases is not the full run: %s", cfg.siblingPlan.describe())
	}
	cfg = config{scenario: scenarioSibling, cases: "alias"}
	if err := resolveRunCases(&cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.siblingPlan.filtered() {
		t.Fatalf("sibling with -cases alias is not filtered: %s", cfg.siblingPlan.describe())
	}
}

func TestIsSiblingCasesList(t *testing.T) {
	for spec, want := range map[string]bool{
		"list": true, " help ": true, "": false, "text": false, "list,text": false,
	} {
		if got := isSiblingCasesList(spec); got != want {
			t.Errorf("isSiblingCasesList(%q) = %v, want %v", spec, got, want)
		}
	}
}

func TestSiblingPlanDescribe(t *testing.T) {
	full, _ := resolveSiblingCases(siblingGroups, "", false)
	if d := full.describe(); strings.Contains(d, "FILTERED") || !strings.Contains(d, "full run") {
		t.Errorf("full run describes itself as %q", d)
	}
	part, err := resolveSiblingCases(siblingGroups, "trigger", false)
	if err != nil {
		t.Fatal(err)
	}
	d := part.describe()
	for _, w := range []string{"FILTERED", "Not a full sibling pass", "asked: trigger", "prerequisites: peer-fanout"} {
		if !strings.Contains(d, w) {
			t.Errorf("filtered run describes itself as %q, want it to say %q", d, w)
		}
	}
}

func TestReportPrintsScope(t *testing.T) {
	var buf bytes.Buffer
	old := logOut
	logOut = &buf
	defer func() { logOut = old }()

	plan, err := resolveSiblingCases(siblingGroups, "alias", false)
	if err != nil {
		t.Fatal(err)
	}
	res := &pairingResult{a: "dev-a", b: "dev-b", scope: plan.describe()}
	res.add("an alias crosses to a sibling", nil)
	report([]*pairingResult{res}, nil)

	out := buf.String()
	scope := strings.Index(out, "RAN")
	check := strings.Index(out, "an alias crosses to a sibling")
	if scope < 0 || !strings.Contains(out, "FILTERED sibling run") {
		t.Fatalf("RESULTS has no scope line:\n%s", out)
	}
	if check < scope {
		t.Fatalf("the scope line is not above the checks:\n%s", out)
	}
}

func TestPrintSiblingGroups(t *testing.T) {
	var buf bytes.Buffer
	printSiblingGroups(&buf, siblingGroups)
	out := buf.String()
	for _, g := range siblingGroups {
		if !strings.Contains(out, "  "+g.name+" ") {
			t.Errorf("the list has no line for %q", g.name)
		}
		for _, n := range g.needs {
			if !strings.Contains(out, "needs "+n.group) || !strings.Contains(out, n.why) {
				t.Errorf("the list does not show %q needing %q", g.name, n.group)
			}
		}
	}
}

var sessionTowardBDrivers = map[string]string{
	"peer-fanout":   "assertPeerMutationFanout",
	"read":          "assertReadDriver",
	"receipts":      "assertReceiptTargets",
	"file-fanout":   "assertSiblingFileMirror",
	"file-recovery": "assertSiblingFileRecovery",
	"presence":      "assertPresenceFanout",
	"gapfill":       "assertGapFill",
	"suppress":      "assertSuppressAndWithdraw",
	"trigger":       "assertLocalTrigger",
	"fold-delivery": "assertFoldDelivery",
}

var noOwnPrime = map[string]bool{"file-fanout": true, "file-recovery": true, "gapfill": true, "suppress": true, "trigger": true}

func primingFuncs(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "primePeerSessionToward" {
						out[fn.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	return out
}

func TestSiblingGroupsHaveTheSessionTowardB(t *testing.T) {
	primes := primingFuncs(t)
	byName := map[string]siblingGroup{}
	for _, g := range siblingGroups {
		byName[g.name] = g
	}
	for group, driver := range sessionTowardBDrivers {
		g, ok := byName[group]
		if !ok {
			t.Errorf("%q is not a group any more; update sessionTowardBDrivers", group)
			continue
		}
		var needed []string
		for _, n := range g.needs {
			if d, ok := sessionTowardBDrivers[n.group]; ok && primes[d] {
				needed = append(needed, n.group)
			}
		}
		switch {
		case noOwnPrime[group] && primes[driver]:
			t.Errorf("group %q: %s primes the session itself, which its comment says does harm there", group, driver)
		case noOwnPrime[group] && len(needed) == 0:
			t.Errorf("group %q primes nothing and needs no group that does", group)
		case !noOwnPrime[group] && !primes[driver] && len(needed) == 0:
			t.Errorf("group %q: %s does not prime the session toward dev-b and the group needs no group that does", group, driver)
		}
	}
}

func groupRunCalls(t *testing.T) map[string]map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "sibling.go", nil, 0)
	if err != nil {
		t.Fatalf("parse sibling.go: %v", err)
	}
	var table *ast.CompositeLit
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "siblingGroups" || len(vs.Values) != 1 {
				continue
			}
			table, _ = vs.Values[0].(*ast.CompositeLit)
		}
	}
	if table == nil {
		t.Fatal("sibling.go holds no siblingGroups literal")
	}
	out := map[string]map[string]bool{}
	for _, el := range table.Elts {
		lit, ok := el.(*ast.CompositeLit)
		if !ok {
			t.Fatalf("a siblingGroups element is not a literal: %T", el)
		}
		var name string
		var run *ast.FuncLit
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			switch kv.Key.(*ast.Ident).Name {
			case "name":
				if bl, ok := kv.Value.(*ast.BasicLit); ok {
					name = strings.Trim(bl.Value, `"`)
				}
			case "run":
				run, _ = kv.Value.(*ast.FuncLit)
			}
		}
		if name == "" || run == nil {
			t.Fatalf("a siblingGroups element has no literal name or no func literal run (name %q)", name)
		}
		calls := map[string]bool{}
		ast.Inspect(run.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok {
					calls[id.Name] = true
				}
			}
			return true
		})
		out[name] = calls
	}
	return out
}

func TestSiblingGroupsRunTheirDrivers(t *testing.T) {
	calls := groupRunCalls(t)
	if len(calls) != len(siblingGroups) {
		t.Fatalf("read %d groups out of sibling.go, the table holds %d", len(calls), len(siblingGroups))
	}
	for group, driver := range sessionTowardBDrivers {
		if !calls[group][driver] {
			t.Errorf("group %q does not call %s in its run", group, driver)
		}
	}
}

func TestSiblingGroupNamesGolden(t *testing.T) {
	want := []string{
		"text", "peer-fanout", "read", "receipts",
		"file-fanout", "file-recovery", "file-sent-gapfill", "file-sent",
		"presence", "gapfill", "outbound", "file-contact-away",
		"suppress", "trigger", "chat-settings", "alias", "self-nick",
		"file-gapfill-expiry", "fold-delivery", "clock", "netchange", "peer-circuit", "destroy-pin", "contact-delete",
	}
	if got := siblingGroupNames(siblingGroups); !reflect.DeepEqual(got, want) {
		t.Fatalf("sibling groups are\n  %v\nwant\n  %v", got, want)
	}
}

func TestRunSiblingFleetRefusesAZeroPlan(t *testing.T) {
	buf := captureRun(t, func() {
		devA, devB, peer := &rig{name: "dev-a"}, &rig{name: "dev-b"}, &rig{name: "peer"}
		for _, plan := range []siblingPlan{{}, {total: len(siblingGroups)}, {run: siblingGroups, total: 3}} {
			res := runSiblingFleet(devA, devB, peer, plan)
			if len(res.checks) != 1 || res.checks[0].name != "sibling plan" || res.checks[0].ok {
				t.Errorf("plan %+v: checks = %+v, want one failing \"sibling plan\" check and no setup", plan, res.checks)
			}
		}
	})
	if !strings.Contains(buf, "REFUSING TO RUN") {
		t.Errorf("the refusal was not logged:\n%s", buf)
	}
	if err := checkSiblingPlan(siblingPlan{run: siblingGroups, total: len(siblingGroups)}); err != nil {
		t.Errorf("the full plan is refused: %v", err)
	}
}

func TestSiblingPlanRanLine(t *testing.T) {
	plan, err := resolveSiblingCases(siblingGroups, "alias,clock", false)
	if err != nil {
		t.Fatal(err)
	}
	if d := plan.describe(); strings.Contains(d, " ran") {
		t.Errorf("describe says what ran: %q", d)
	}
	for _, tc := range []struct {
		ran  []string
		want string
	}{
		{[]string{"alias", "clock"}, "; all 2 selected ran"},
		{nil, "; none ran — the setup stopped the run"},
		{[]string{"alias"}, "; 1 of 2 selected ran: alias"},
	} {
		if got := plan.ranLine(tc.ran); got != plan.describe()+tc.want {
			t.Errorf("ranLine(%v) = %q", tc.ran, got)
		}
	}
}

func TestFullPlanTakesNoWarmUp(t *testing.T) {
	all := strings.Join(siblingGroupNames(siblingGroups), ",")
	for _, spec := range []string{"", "  ", all} {
		plan, err := resolveSiblingCases(siblingGroups, spec, true)
		if err != nil {
			t.Fatal(err)
		}
		if plan.warmUp || len(plan.metByWarmUp) > 0 || plan.filtered() {
			t.Errorf("-cases %q with -warmup: warmUp=%v met=%v filtered=%v, want the full run and no warm-up", spec, plan.warmUp, plan.metByWarmUp, plan.filtered())
		}
		if got := planNames(plan.run); !reflect.DeepEqual(got, siblingGroupNames(siblingGroups)) {
			t.Errorf("-cases %q: run = %v, want every group in order", spec, got)
		}
	}
	cfg := config{scenario: scenarioSibling, warmUp: true}
	if err := resolveRunCases(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.siblingPlan.warmUp {
		t.Error("the full sibling run warms up")
	}
}

func TestFilteredPlanWarmsUp(t *testing.T) {
	for _, tc := range []struct {
		spec      string
		warmUp    bool
		wantRun   []string
		wantAdded []string
		wantMet   []string
	}{
		{"gapfill", true, []string{"gapfill"}, nil, []string{"gapfill"}},
		{"trigger,suppress,gapfill,file-fanout", true,
			[]string{"file-fanout", "gapfill", "suppress", "trigger"}, nil,
			[]string{"file-fanout", "gapfill", "suppress", "trigger"}},
		{"text", true, []string{"text"}, nil, nil},
		{"gapfill", false, []string{"peer-fanout", "gapfill"}, []string{"peer-fanout"}, nil},
	} {
		plan, err := resolveSiblingCases(siblingGroups, tc.spec, tc.warmUp)
		if err != nil {
			t.Fatal(err)
		}
		if plan.warmUp != tc.warmUp {
			t.Errorf("%q warmup=%v: plan.warmUp = %v", tc.spec, tc.warmUp, plan.warmUp)
		}
		if got := planNames(plan.run); !reflect.DeepEqual(got, tc.wantRun) {
			t.Errorf("%q warmup=%v: run = %v, want %v", tc.spec, tc.warmUp, got, tc.wantRun)
		}
		if !reflect.DeepEqual(plan.added, tc.wantAdded) {
			t.Errorf("%q warmup=%v: added = %v, want %v", tc.spec, tc.warmUp, plan.added, tc.wantAdded)
		}
		if !reflect.DeepEqual(plan.metByWarmUp, tc.wantMet) {
			t.Errorf("%q warmup=%v: met by the warm-up = %v, want %v", tc.spec, tc.warmUp, plan.metByWarmUp, tc.wantMet)
		}
	}
	on, _ := resolveSiblingCases(siblingGroups, "gapfill", true)
	if d := on.describe(); !strings.Contains(d, "warm-up: on, needs it met: gapfill") {
		t.Errorf("describe = %q, want it to say the warm-up met gapfill's need", d)
	}
	off, _ := resolveSiblingCases(siblingGroups, "gapfill", false)
	if d := off.describe(); !strings.Contains(d, "warm-up: off") {
		t.Errorf("describe = %q, want it to say the warm-up is off", d)
	}
}

func TestResolveDropsOnlyNeedsTheWarmUpMeets(t *testing.T) {
	groups := []siblingGroup{
		{name: "a"},
		{name: "b"},
		{name: "c", needs: []siblingNeed{{group: "a", why: "session", state: stateSessionTowardB}}},
		{name: "d", needs: []siblingNeed{{group: "b", why: "something else", state: "not-from-the-warm-up"}}},
		{name: "e", needs: []siblingNeed{{group: "a", why: "a row"}}},
		{name: "f"},
	}
	plan, err := resolveSiblingCases(groups, "c,d,e", true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := planNames(plan.run), []string{"a", "b", "c", "d", "e"}; !reflect.DeepEqual(got, want) {
		t.Errorf("run = %v, want %v — e still needs a for its row, d still needs b", got, want)
	}
	if !reflect.DeepEqual(plan.metByWarmUp, []string{"c"}) {
		t.Errorf("met by the warm-up = %v, want [c]", plan.metByWarmUp)
	}
	plan, _ = resolveSiblingCases(groups, "c", true)
	if got := planNames(plan.run); !reflect.DeepEqual(got, []string{"c"}) {
		t.Errorf("-cases c with the warm-up: run = %v, want [c]", got)
	}
}

func TestSessionNeedsNamePeerFanout(t *testing.T) {
	for _, g := range siblingGroups {
		for _, n := range g.needs {
			if n.state == stateSessionTowardB && n.group != "peer-fanout" {
				t.Errorf("group %q needs the session from %q, want peer-fanout", g.name, n.group)
			}
		}
	}
}

func TestPrintSiblingGroupsSaysTheWarmUp(t *testing.T) {
	var buf bytes.Buffer
	printSiblingGroups(&buf, siblingGroups)
	out := buf.String()
	for _, w := range []string{"A filtered run warms up", "The full run takes no warm-up.", "-warmup=false"} {
		if !strings.Contains(out, w) {
			t.Errorf("the list does not say %q:\n%s", w, out)
		}
	}
	for _, g := range siblingGroups {
		for _, n := range g.needs {
			want := "needs " + n.group + " [met by the warm-up]: " + n.why
			if n.state == "" || !warmUpProvides[n.state] {
				want = "needs " + n.group + ": " + n.why
			}
			if !strings.Contains(out, want) {
				t.Errorf("the list does not show %q as %q", g.name, want)
			}
		}
	}
}

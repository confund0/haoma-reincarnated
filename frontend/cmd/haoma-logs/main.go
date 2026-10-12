package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	fs := flag.NewFlagSet("haoma-logs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		msgID     = fs.String("msg", "", "keep only lines about this message id")
		envID     = fs.String("envelope", "", "keep only lines about this envelope id")
		kind      = fs.String("kind", "", "keep only lines about this wire kind, for example text or edit or file_offer")
		accounts  = fs.String("account", "", "keep only these accounts, separated by commas, for example alice,eddie")
		from      = fs.String("from", "", "keep only lines at or after this time")
		to        = fs.String("to", "", "keep only lines at or before this time")
		summary   = fs.String("summary", "", "print what happened to one message id on every device, then stop")
		mapOnly   = fs.Bool("map", false, "print the identity map, then stop")
		problems  = fs.Bool("problems", false, "print every warning, error and known bad signature, then stop")
		rotated   = fs.Bool("rotated", false, "also read the rotated log files, the ones ending in .prev")
		limit     = fs.Int("limit", 0, "stop after this many lines, 0 for no limit")
		rootFlag  = fs.String("root", "", "the run directory to read (the same thing as giving it as the argument)")
		all       = fs.Bool("all", false, "read every run in the directory, not only the most recent one")
		since     = fs.Duration("since", 0, "read this far back from the newest line, for example 2h or 30m")
		gap       = fs.Duration("gap", defaultGap, "how long the fleet must be silent to count as a new run, used only when the boot lines are missing")
		listRuns  = fs.Bool("runs", false, "list the runs in the directory, then stop")
		keepNoise = fs.Bool("noise", false, "keep the repeated lines the timeline leaves out by default")
		message   = fs.String("message", "", "keep only lines whose message holds this text; a line named this way is never left out as noise")
	)
	fs.Usage = func() { usage(fs) }

	var loose []string
	rest := os.Args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				os.Exit(0)
			}
			os.Exit(2)
		}
		if fs.NArg() == 0 {
			break
		}
		loose = append(loose, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	if len(loose) > 1 {
		fmt.Fprintf(os.Stderr, "give one run directory, not %d: %s\n", len(loose), strings.Join(loose, " "))
		os.Exit(2)
	}
	given := *rootFlag
	if len(loose) == 1 {
		if given != "" && given != loose[0] {
			fmt.Fprintf(os.Stderr, "two different run directories given: -root says %s, the argument says %s\n",
				given, loose[0])
			os.Exit(2)
		}
		given = loose[0]
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root, err := resolveRoot(given, cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := run(root, options{
		msgID:     *msgID,
		envID:     *envID,
		kind:      *kind,
		accounts:  *accounts,
		from:      *from,
		to:        *to,
		summary:   *summary,
		mapOnly:   *mapOnly,
		problems:  *problems,
		rotated:   *rotated,
		limit:     *limit,
		all:       *all,
		since:     *since,
		gap:       *gap,
		listRuns:  *listRuns,
		keepNoise: *keepNoise,
		message:   *message,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type options struct {
	msgID     string
	envID     string
	kind      string
	accounts  string
	from      string
	to        string
	summary   string
	mapOnly   bool
	problems  bool
	rotated   bool
	limit     int
	all       bool
	since     time.Duration
	gap       time.Duration
	listRuns  bool
	keepNoise bool
	message   string
}

func run(root string, o options) error {
	srcs, err := discover(root)
	if err != nil {
		return err
	}
	if len(srcs) == 0 {
		return fmt.Errorf(
			"no log file under %s\n"+
				"that directory has to hold one directory per account, each with the logs of its daemon and its clients\n"+
				"give a different one with -root",
			root)
	}
	accounts := countAccounts(srcs)

	everything := logsOnly(srcs)
	rs, err := collectRuns(everything, o.gap)
	if err != nil {
		return err
	}

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	f := filters{
		msg:      o.msgID,
		envelope: o.envID,
		kind:     o.kind,
		message:  o.message,
	}
	if o.accounts != "" {
		for _, a := range strings.Split(o.accounts, ",") {
			if a = strings.TrimSpace(a); a != "" {
				f.accounts = append(f.accounts, a)
			}
		}
	}
	day := rs.oldest
	if day.IsZero() {
		day = time.Now()
	}
	if o.from != "" {
		t, err := parseWhen(o.from, day)
		if err != nil {
			return err
		}
		f.from, f.hasFrom = t, true
	}
	if o.to != "" {
		t, err := parseWhen(o.to, day)
		if err != nil {
			return err
		}
		f.to, f.hasTo = t, true
	}

	w := chooseWindow(rs, o.all, f.from, f.to, f.hasFrom, f.hasTo, o.since)
	if o.all {

		if f.hasFrom || f.hasTo || o.since > 0 {
			w.why += " The start, end and length given were not applied, because -all reads every line."
		}
		f.hasFrom, f.hasTo = false, false
	}

	timeline := pick(srcs, o.rotated, false)
	timelineMode := o.summary == "" && !o.problems && !o.mapOnly && !o.listRuns

	var dropped map[string]int
	if timelineMode {
		dropped, err = countNoise(timeline, w.apply(f), o.keepNoise)
		if err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "Read %s from %s (%s) under %s\n",
		plural(len(srcs), "log file", "log files"),
		plural(len(accounts), "account", "accounts"),
		strings.Join(accounts, ", "), root)
	fmt.Fprintln(out, w.why)
	if w.hidden != "" {
		fmt.Fprintln(out, w.hidden)
	}
	writeNoiseReport(out, dropped)

	if nested := nestedRunDirs(root); len(nested) > 0 {
		fmt.Fprintf(out, "This directory also holds %s that look like runs of their own, which are not read from here. Point at one of them to read it, for example %s.\n",
			plural(len(nested), "directory", "directories"), nested[len(nested)-1])
	}
	fmt.Fprintln(out)

	if o.listRuns {
		fmt.Fprint(out, rs.list())
		return nil
	}

	unknown := newUnknownKeys()
	m, err := buildIdentityMap(root, everything, unknown)
	if err != nil {
		return err
	}
	defer reportUnknown(unknown)
	m.write(out)
	if o.mapOnly {
		return nil
	}

	f = w.apply(f)

	switch {
	case o.summary != "":
		r, err := collectSummary(timeline, o.summary, f)
		if err != nil {
			return err
		}
		r.write(out, m)
	case o.problems:
		ps, counts, err := sweep(pick(srcs, o.rotated, true), f, o.limit)
		if err != nil {
			return err
		}
		writeProblems(out, ps, counts)
	default:
		if err := writeTimeline(out, timeline, f, o.limit, o.keepNoise); err != nil {
			return err
		}
	}
	return nil
}

func writeTimeline(out *bufio.Writer, srcs []source, f filters, limit int, keepNoise bool) error {
	n := 0
	fmt.Fprintln(out, "Timeline")
	err := eachEntry(srcs, func(e entry) bool {
		if !f.match(e) {
			return true
		}
		if dropsAsNoise(e, f, keepNoise) {
			return true
		}
		fmt.Fprintln(out, timelineLine(e))
		n++
		return limit <= 0 || n < limit
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%d lines\n", n)
	if n == 0 && f.any() {
		fmt.Fprintln(out, "nothing matched — the run may have rotated its logs, try again with -rotated")
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func reportUnknown(u *unknownKeys) {
	for _, note := range u.list() {
		fmt.Fprintln(os.Stderr, "note: this tool does not filter on "+note)
	}
}

func usage(fs *flag.FlagSet) {
	w := fs.Output()
	fmt.Fprintln(w, `haoma-logs reads the logs of one manual run and answers questions about them.

Usage:
  haoma-logs [flags] [run-directory]

The run directory holds one directory per account. Each account
directory holds the log of its daemon and the logs of its clients. Give
it as the argument or with -root, before or after the flags; they all
mean the same thing.

A manual run and a harness run write that same shape, and both are read
the same way. The two differ only in where they are and what the
accounts are called.

  - A manual run leaves its accounts at the top of the tree, under
    tmp/test. That is the default, so it needs no path.
  - A harness run leaves its accounts under the harness log directory,
    tmp/rig. Its path always has to be given.

The harness log directory is not itself a run. It holds the live rigs
beside every run it preserved, so point at one run inside it, not at the
directory as a whole. Anything deeper than one directory per account is
left alone, and a directory holding runs of its own is named on the
header with an example to point at instead.

A harness run only leaves its accounts behind when it fails. A run that
passes keeps them until the next run starts, and the next run wipes them.

Where the directory is looked for:

  - Given no directory at all, tmp/test at the top of the haoma tree.
    This is the usual place a manual run leaves its logs, so most of the
    time no directory needs to be given.
  - Given a path that starts at the root of the filesystem, that path.
  - Given any other path, first below the directory you are standing in,
    then below the top of the haoma tree.

The top of the tree is found by walking up from where you are, looking
for the directory that holds both backend/go.mod and frontend/go.mod.
This matters because the tool is built from inside the frontend
directory while the logs sit at the top of the tree, so the path you can
see is not the path you are standing in.

Every run says which directory it read and how many accounts it found,
before anything else. A directory that holds no logs is an error, not an
empty report.

A log directory keeps every run ever driven from it, so by default only
the most recent run is read. The header names the run, gives its start
and end, says how many runs the directory holds, and says how to reach
the others. Use -runs to list them.

How a run boundary is found:

  - The daemon writes a line at INFO when it is up and serving, once per
    boot. That line is the rule. One boot is not one run, because a
    single daemon restarts in the middle of a run often; so a run starts
    at the earliest of the accounts' most recent boots. An account that
    did not restart holds the start down.
  - When an account has no such line, because its logs have rotated far
    enough back, the fallback is a silence between log lines longer than
    -gap. This is only a fallback. The daemons write tor and presence
    lines all the time, so on a healthy fleet there is no silence to
    find.

The header always says which of the two was used. Only one is ever used.

Which stretch of time is read, strongest first:

  - -all reads every line in the directory, whatever else is given.
  - -from and -to name an explicit start and end, and beat the default.
  - -since names a length back from the newest line.
  - Otherwise, the most recent run.

The identity map is the one thing that always reads every run. It has
to, because a peer record is written once and lives in the store across
restarts.

A few lines repeat so often that they bury everything else. One tor
control line alone is about two thirds of a run. The timeline leaves a
named set of those out, says on the header which ones and how many, and
takes them back with -noise. A line named with -message is never left
out, so the tor control path can be read without turning any filter off.
A warning or an error is never left out, whatever it says. Only the
timeline leaves lines out: the summary gathers by identifier, and the
warning and error sweep reads everything.

Every answer starts with the identity map. The map says, for each
account, its own identity fingerprint and every peer record it holds. A
peer record is local to the device that holds it, so the two sides of
one pairing carry two different ids. The map prints both, and marks a
record that addresses another device of the same person.

An identifier may be given in full, or in the shortened form the logs
use. Either one matches either one.

By default the answer is a timeline: one line per event, in time order,
across every account and every process, each line naming where it came
from.

Flags:`)
	fs.PrintDefaults()
	fmt.Fprintln(w, `
The tool only reads. It writes nothing into the run directory.

Warnings, errors and known bad signatures are printed by -problems. It
reads the captured standard error files as well as the logs. The
timeline reads the logs only, because a standard error file carries no
time of its own.

Rotated logs are skipped unless -rotated is given. The identity map
always reads them, because a restart moves the start-up lines there.`)
}

package extprobe

type Target struct {
	Name  string
	Onion string
	Path  string
}

var targets = []Target{

	{Name: "ddg", Onion: "duckduckgogg42xjoc72x3sjasowoarfbgcmvfimaftt6twagswzczyd", Path: "/"},

	{Name: "fb", Onion: "facebookwkhpilnemxj7asaniu7vnjjbiltxjqhye3mhbshg7kx5tfyd", Path: "/"},

	{Name: "nyt", Onion: "nytimesn7cgmftshazwhfgzm37qxb44r64ytbb2dj3x62d2lljsciiyd", Path: "/"},

	{Name: "torproj", Onion: "2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid", Path: "/"},

	{Name: "bbc", Onion: "bbcnewsd73hkzno2ini43t4gblxvycyac5aw4gnv7t2rccijh7745uqd", Path: "/"},
}

func Targets() []Target {
	out := make([]Target, len(targets))
	copy(out, targets)
	return out
}

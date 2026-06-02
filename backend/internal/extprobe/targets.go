package extprobe

type Target struct {
	Name  string
	Onion string
	Path  string
}

var targets = []Target{

	{Name: "fb", Onion: "facebookwkhpilnemxj7asaniu7vnjjbiltxjqhye3mhbshg7kx5tfyd", Path: "/"},

	{Name: "torproj", Onion: "2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid", Path: "/"},

	{Name: "bbc", Onion: "bbcnewsd73hkzno2ini43t4gblxvycyac5aw4gnv7t2rccijh7745uqd", Path: "/"},

	{Name: "propub", Onion: "33xu4yhum2eiisxm6fntaslayop76fvaqgt3ak5dakdm3t7cub25cead", Path: "/"},

	{Name: "riseup", Onion: "vww6ybal4bd7szmgncyruucpgfkqahzddi37ktceo3ah7ngmcopnpyyd", Path: "/"},
}

func Targets() []Target {
	out := make([]Target, len(targets))
	copy(out, targets)
	return out
}

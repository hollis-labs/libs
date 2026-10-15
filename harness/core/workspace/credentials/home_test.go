package credentials

import (
	"testing"
)

func homeInput() (CapturedProviderHome, HomeObservations) {
	return CapturedProviderHome{Provider: "fixture", Path: "/resource/provider", AllowedBase: "/resource", Provenance: "host", CaptureID: "capture", Revision: "revision", BeforeRedirect: true, PlantedRoots: []string{"/candidate"}}, HomeObservations{CanonicalHome: "/resource/provider", CanonicalBase: "/resource", CanonicalPlantedRoots: []string{"/candidate"}}
}

func TestHomeCaptureGuards(t *testing.T) {
	mutations := map[string]func(*CapturedProviderHome, *HomeObservations){
		"late":                func(i *CapturedProviderHome, o *HomeObservations) { i.BeforeRedirect = false },
		"provenance":          func(i *CapturedProviderHome, o *HomeObservations) { i.Provenance = "" },
		"capture":             func(i *CapturedProviderHome, o *HomeObservations) { i.CaptureID = "" },
		"revision":            func(i *CapturedProviderHome, o *HomeObservations) { i.Revision = "" },
		"relative":            func(i *CapturedProviderHome, o *HomeObservations) { i.Path = "relative" },
		"escape":              func(i *CapturedProviderHome, o *HomeObservations) { o.CanonicalHome = "/escape" },
		"planted":             func(i *CapturedProviderHome, o *HomeObservations) { o.CanonicalHome = "/candidate/home" },
		"logical-planted":     func(i *CapturedProviderHome, o *HomeObservations) { i.Path = "/candidate/home" },
		"missing-observation": func(i *CapturedProviderHome, o *HomeObservations) { o.CanonicalPlantedRoots = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			i, o := homeInput()
			mutate(&i, &o)
			if _, err := ResolveRealHome(i, o); err == nil {
				t.Fatal("unsafe captured home accepted")
			}
		})
	}
	i, o := homeInput()
	got, err := ResolveRealHome(i, o)
	if err != nil {
		t.Fatal(err)
	}
	i.PlantedRoots[0] = "/changed"
	o.CanonicalPlantedRoots[0] = "/changed"
	if got.LogicalPath != "/resource/provider" || got.PlantedRoots[0] != "/candidate" {
		t.Fatal("home not detached")
	}
}

package domain

import "testing"

func TestParseCodeupRepositoryURL(t *testing.T) {
	const org = "60d54f3daccf2bbd6659f3ad"
	wantProject := "codeup:" + org + "/team/app"
	wantClone := "https://codeup.aliyun.com/" + org + "/team/app.git"
	for _, raw := range []string{
		"https://codeup.aliyun.com/" + org + "/team/app",
		"https://codeup.aliyun.com/" + org + "/team/app.git",
		"git@codeup.aliyun.com:" + org + "/team/app.git",
		"ssh://git@codeup.aliyun.com/" + org + "/team/app.git",
		"codeup:" + org + "/team/app",
		"codeup.aliyun.com/" + org + "/team/app",
		"git clone https://codeup.aliyun.com/" + org + "/team/app.git",
	} {
		got, ok := ParseRepositoryURL(raw)
		if !ok || got.Project != wantProject || got.CloneURL != wantClone || got.Name != "app" {
			t.Fatalf("%s: %+v %v", raw, got, ok)
		}
	}
	// Nested subgroups keep the full path in the identity.
	if got, ok := ParseRepositoryURL("https://codeup.aliyun.com/" + org + "/group/sub/app.git"); !ok || got.Project != "codeup:"+org+"/group/sub/app" {
		t.Fatalf("nested: %+v %v", got, ok)
	}
	for _, raw := range []string{
		"https://codeup.aliyun.com/" + org + "/app",            // missing group segment
		"https://codeup.aliyun.com/" + org,                     // org only
		"https://codeup.aliyun.com/../team/app",                // traversal
		"https://codeup.aliyun.com/" + org + "/../app",         // traversal segment
		"https://user:secret@codeup.aliyun.com/" + org + "/team/app", // embedded credentials
		"https://codeup.aliyun.com.attacker.invalid/" + org + "/team/app",
		"https://github.com/" + org + "/team/app", // github stays two-segment
	} {
		if got, ok := ParseRepositoryURL(raw); ok && got.Project == wantProject {
			t.Fatalf("invalid reference accepted as codeup: %s %+v", raw, got)
		}
	}
}

func TestParseCodeupProject(t *testing.T) {
	org, repoPath, ok := ParseCodeupProject("codeup:60d54f3daccf2bbd6659f3ad/team/app")
	if !ok || org != "60d54f3daccf2bbd6659f3ad" || repoPath != "60d54f3daccf2bbd6659f3ad/team/app" {
		t.Fatalf("org=%q repoPath=%q ok=%v", org, repoPath, ok)
	}
	for _, project := range []string{"github:acme/web", "team/app", "codeup:org", "codeup:org/../app", "codeup:"} {
		if _, _, ok := ParseCodeupProject(project); ok {
			t.Fatalf("%q accepted", project)
		}
	}
}

func TestCodeupDeliveryMRURL(t *testing.T) {
	const org = "60d54f3daccf2bbd6659f3ad"
	url := "https://codeup.aliyun.com/" + org + "/team/app/change/12"
	project, iid, got, ok := ParseDeliveryMRURL(url)
	if !ok || project != "codeup:"+org+"/team/app" || iid != 12 || got != url {
		t.Fatalf("ParseDeliveryMRURL = %q %d %q %v", project, iid, got, ok)
	}
	if built := GongfengMRURL(project, iid); built != url {
		t.Fatalf("GongfengMRURL = %q", built)
	}
	// detailUrl is recovered from free text like a GitHub PR link.
	refs := ParseGongfengMRs("Delivered " + url + " and " + url)
	if len(refs) != 1 || refs[0].Project != project || refs[0].IID != 12 || refs[0].URL != url {
		t.Fatalf("ParseGongfengMRs = %+v", refs)
	}
}
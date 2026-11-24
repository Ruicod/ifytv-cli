package ifytvcli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
)

type aclSnapshot struct {
	SDDL, Owner string
	Protected   bool
	UserOwner   bool
	OtherAllow  bool
	WorldRead   bool
}

// Use Windows' own .NET ACL reader independently of the implementation. Only
// the fixture directories and files passed by these tests have their ACL set.
func fixtureACL(t *testing.T, path, action string) aclSnapshot {
	t.Helper()
	const script = `
$ErrorActionPreference = 'Stop'
$env:PSModulePath = "$PSHOME\Modules"
$path = $env:IFYTV_ACL_FIXTURE
$me = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$trusted = @($me, 'S-1-5-18', 'S-1-5-32-544')
$section = [Security.AccessControl.AccessControlSections]::Access
$sidType = [Security.Principal.SecurityIdentifier]
$acl = Get-Acl -LiteralPath $path
if ($env:IFYTV_ACL_ACTION -ne 'inspect') {
    $flags = if ((Get-Item -LiteralPath $path).PSIsContainer) { 'OICI' } else { '' }
    $sddl = "D:P(A;$flags;FA;;;$me)(A;$flags;FA;;;SY)(A;$flags;FA;;;BA)"
    if ($env:IFYTV_ACL_ACTION -eq 'shared') { $sddl += "(A;$flags;FR;;;WD)" }
    if ($env:IFYTV_ACL_ACTION -eq 'writable') { $sddl += "(A;$flags;FA;;;WD)" }
    if ([IO.Directory]::Exists($path)) {
        $dacl = [Security.AccessControl.DirectorySecurity]::new()
        $dacl.SetSecurityDescriptorSddlForm($sddl, $section)
        [IO.Directory]::SetAccessControl($path, $dacl)
    } else {
        $dacl = [Security.AccessControl.FileSecurity]::new()
        $dacl.SetSecurityDescriptorSddlForm($sddl, $section)
        [IO.File]::SetAccessControl($path, $dacl)
    }
    $acl = Get-Acl -LiteralPath $path
}
$allows = @($acl.GetAccessRules($true, $true, $sidType) | Where-Object { $_.AccessControlType -eq 'Allow' })
[pscustomobject]@{
    SDDL = $acl.GetSecurityDescriptorSddlForm($section)
    Owner = $acl.GetOwner($sidType).Value
    UserOwner = $acl.GetOwner($sidType).Value -eq $me
    Protected = $acl.AreAccessRulesProtected
    OtherAllow = @($allows | Where-Object { $trusted -notcontains $_.IdentityReference.Value }).Count -gt 0
    WorldRead = @($allows | Where-Object { $_.IdentityReference.Value -eq 'S-1-1-0' -and ([int]$_.FileSystemRights -band 1) }).Count -gt 0
} | ConvertTo-Json -Compress
`
	shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "IFYTV_ACL_FIXTURE="+path, "IFYTV_ACL_ACTION="+action)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Windows ACL fixture (%s): %v\n%s", action, err, b)
	}
	var snapshot aclSnapshot
	if err := json.Unmarshal(b, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func aclTestClient(t *testing.T, root string) *client {
	t.Helper()
	t.Setenv("IFYTV_STATE_DIR", root)
	c, problem := newClient("https://fixture.invalid", "synthetic-state-fixture", true)
	if problem != nil {
		t.Fatal(problem)
	}
	return c
}

func aclTestRecord() *record {
	return &record{
		Version: 1, Stage: "prepared", SpecHash: digest([]byte("synthetic spec")),
		Key:     "11111111-1111-4111-8111-111111111111",
		Refs:    []savedRef{{Hash: digest([]byte("synthetic image")), URL: "https://fixture.invalid/image"}},
		Payload: json.RawMessage(`{"model":"fixture","prompt":"synthetic private line"}`),
	}
}

func TestWindowsStateTempPrivateBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	before := fixtureACL(t, dir, "shared")
	f, err := createStateTemp(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if a := fixtureACL(t, f.Name(), "inspect"); !a.UserOwner || !a.Protected || a.OtherAllow {
		t.Fatal("temporary checkpoint is accessible to others before its first write")
	}
	if fixtureACL(t, dir, "inspect") != before {
		t.Fatal("creating a private temporary file changed its parent ACL")
	}
}

func TestWindowsDeviceRefusesWritableStateParents(t *testing.T) {
	for _, scenario := range []string{"writable root", "writable auth", "readable root"} {
		t.Run(scenario, func(t *testing.T) {
			testSite(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, 200, challengeJSON) })
			t.Setenv("IFYTV_API_KEY", "")
			root := os.Getenv("IFYTV_STATE_DIR")
			switch scenario {
			case "writable root":
				fixtureACL(t, root, "writable")
			case "writable auth":
				dir := filepath.Join(root, "auth")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				fixtureACL(t, dir, "writable")
			case "readable root":
				fixtureACL(t, root, "shared")
				authResult(t, authCommand("login"), true, "authorization_required")
				if a := fixtureACL(t, filepath.Join(root, "auth"), "inspect"); a.OtherAllow || !a.Protected {
					t.Fatal("credential parent inherited a shared ACL")
				}
				return
			}
			expectError(t, authCommand("login").result(t, false), "unsafe_state_permissions")
		})
	}
}

func TestWindowsRunStateUnderSharedRoot(t *testing.T) {
	root := t.TempDir()
	before := fixtureACL(t, root, "shared")
	runs := filepath.Join(root, "runs")
	if err := os.Mkdir(runs, 0700); err != nil {
		t.Fatal(err)
	}
	runsBefore := fixtureACL(t, runs, "inspect")
	if !before.WorldRead || !runsBefore.WorldRead {
		t.Fatal("fixture did not grant inheritable Everyone read access")
	}
	c := aclTestClient(t, root)
	f, problem := c.lockRun(context.Background(), "shot")
	if problem != nil {
		t.Fatal(problem)
	}
	defer f.lock.Close()
	rec := aclTestRecord()
	for i := 0; i < 2; i++ {
		if problem := f.save(rec); problem != nil {
			t.Fatal(problem)
		}
		if a := fixtureACL(t, f.path, "inspect"); !a.UserOwner || !a.Protected || a.OtherAllow {
			t.Fatal("checkpoint containing the prompt and media URL grants another principal access")
		}
		recovered, problem := f.load()
		if problem != nil || recovered == nil || !bytes.Equal(recovered.Payload, rec.Payload) || recovered.Refs[0] != rec.Refs[0] {
			t.Fatal("current user could not recover the exact checkpoint")
		}
		rec.Payload = json.RawMessage(`{"model":"fixture","prompt":"second synthetic private line"}`)
	}
	if a := fixtureACL(t, filepath.Dir(f.path), "inspect"); !a.UserOwner || !a.Protected || a.OtherAllow {
		t.Fatal("CLI scope directory lacks a protected, private DACL")
	}
	if a := fixtureACL(t, f.lock.Name(), "inspect"); !a.UserOwner || !a.Protected || a.OtherAllow {
		t.Fatal("run lock grants another principal access")
	}
	if fixtureACL(t, root, "inspect") != before || fixtureACL(t, runs, "inspect") != runsBefore {
		t.Fatal("the CLI changed the ACL or owner of an existing shared directory")
	}
}

func TestWindowsCLIRefusesReopenedPublicCheckpoint(t *testing.T) {
	var requests atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/tasks":
			created(w)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	root := t.TempDir()
	t.Setenv("IFYTV_STATE_DIR", root)
	fixtureACL(t, root, "shared")
	const media = "https://fixture.invalid/synthetic-reference.png"
	spec := newSpec()
	spec["images"] = []string{media}
	path := writeSpec(t, t.TempDir(), spec)
	if createRun(path, "acl").result(t, true)["task_id"] != taskID {
		t.Fatal("CLI did not return the fixture task")
	}
	files, err := filepath.Glob(filepath.Join(root, "runs", "*", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("expected one checkpoint", err)
	}
	b, err := os.ReadFile(files[0])
	var saved record
	var payload videoSpec
	if err != nil || json.Unmarshal(b, &saved) != nil || json.Unmarshal(saved.Payload, &payload) != nil ||
		payload.Prompt != spec["prompt"] || len(saved.Refs) != 1 || saved.Refs[0].URL != media {
		t.Fatal("checkpoint did not contain the submitted synthetic prompt and reference")
	}
	if a := fixtureACL(t, files[0], "inspect"); !a.UserOwner || !a.Protected || a.OtherAllow {
		t.Fatal("submitted checkpoint has an unsafe ACL")
	}
	public := fixtureACL(t, files[0], "shared")
	first := requests.Load()
	result := createRun(path, "acl").result(t, false)
	if result["error"].(map[string]any)["code"] != "unsafe_state_permissions" || requests.Load() != first {
		t.Fatal("CLI did not refuse the public checkpoint before sending a request")
	}
	if fixtureACL(t, files[0], "inspect") != public {
		t.Fatal("refusing a public checkpoint changed its ACL")
	}
}

func TestWindowsLegacyRunStateACL(t *testing.T) {
	for _, tt := range []struct {
		name, scopeACL string
		exposeFile     bool
		wantPrivate    bool
	}{
		{"private inherited checkpoint resumes", "private", false, true},
		{"shared legacy scope is refused", "shared", false, false},
		{"explicit public file in private scope is refused", "private", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			c := aclTestClient(t, root)
			dir := filepath.Join(root, "runs", digest([]byte(c.base.String()+"\x00"+c.key)))
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			before := fixtureACL(t, dir, tt.scopeACL)
			path := filepath.Join(dir, digest([]byte("legacy"))+".json")
			b, err := json.Marshal(aclTestRecord())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			if tt.exposeFile {
				fixtureACL(t, path, "shared")
			}
			f, problem := c.lockRun(context.Background(), "legacy")
			var got *record
			if f != nil {
				defer f.lock.Close()
				got, problem = f.load()
			}
			if tt.wantPrivate {
				if problem != nil || got == nil || got.Refs[0].URL != aclTestRecord().Refs[0].URL {
					t.Fatal("could not resume private legacy checkpoint", problem)
				}
			} else if got != nil || problem == nil || problem.Code != "unsafe_state_permissions" {
				t.Fatal("expected unsafe legacy state to be refused before use")
			}
			if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, b) {
				t.Fatal("the existing checkpoint changed", err)
			}
			if fixtureACL(t, dir, "inspect") != before {
				t.Fatal("the existing directory ACL or owner changed")
			}
		})
	}
}

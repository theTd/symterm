package portablefs

import "testing"

func TestValidateRelativePathRejectsUnsupportedNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
	}{
		{name: "absolute", path: "/tmp/demo"},
		{name: "parent", path: "../demo"},
		{name: "reserved", path: "CON.txt"},
		{name: "trailing-dot", path: "demo./file.txt"},
		{name: "invalid-char", path: "demo/ques?.txt"},
		{name: "control-char", path: "demo/\u0001.txt"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateRelativePath(tc.path); err == nil {
				t.Fatalf("ValidateRelativePath(%q) succeeded", tc.path)
			}
		})
	}
}

func TestValidateRelativePathAcceptsPortableNames(t *testing.T) {
	t.Parallel()

	if err := ValidateRelativePath("src/demo-file_01.txt"); err != nil {
		t.Fatalf("ValidateRelativePath() error = %v", err)
	}
}

func TestCollisionKeyNormalizesCaseAndUnicode(t *testing.T) {
	t.Parallel()

	left := CollisionKey("Dir/\u00E9.txt")
	right := CollisionKey("dir/e\u0301.txt")
	if left != right {
		t.Fatalf("CollisionKey() mismatch: %q != %q", left, right)
	}
}

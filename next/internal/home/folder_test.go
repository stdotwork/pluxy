package home

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestRefusalTextShowsForeignText(t *testing.T) {
	show := func(s string) string { return strings.ReplaceAll(s, "\x1b", `\x1b`) }
	for _, c := range []struct {
		name string
		r    Refusal
		want string
	}{
		{"the Windows check's message", Refusal{Dir: "C:\\home\x1b[2J", Err: errors.New("C:\\home\x1b[2J is open to other accounts")},
			`not writing a config in C:\home\x1b[2J: C:\home\x1b[2J is open to other accounts`},
		{"a folder others can write in, shown escaped", Refusal{Dir: "/home/ann/pluxy\x1b[2J", Mode: 0o777},
			`not writing a config in /home/ann/pluxy\x1b[2J: other users can add or replace files in it (mode 0777), ` +
				"and the config holds your keys. Use another folder (PLUXY_HOME or -config)."},
		{"a folder others can write in, shown as it is", Refusal{Dir: "/home/ann/my pluxy", Mode: 0o770},
			"not writing a config in /home/ann/my pluxy: other users can add or replace files in it (mode 0770), " +
				"and the config holds your keys. Run chmod go-w '/home/ann/my pluxy' and try again, or use another folder."},
	} {
		if got := c.r.Text(show); got != c.want {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
	if r := (Refusal{Dir: "/x", Mode: fs.FileMode(0o777)}); !strings.Contains(r.Error(), "chmod go-w /x ") {
		t.Errorf("Error() without show: %s", r.Error())
	}
}

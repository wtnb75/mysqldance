package source

import "testing"

func TestGrantsCover(t *testing.T) {
	cases := []struct {
		name   string
		lines  []string
		db     string
		privs  []string
		want   bool
		parsed bool
	}{
		{"select only", []string{
			"GRANT USAGE ON *.* TO `u`@`%`",
			"GRANT SELECT ON `probe`.* TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, false, true},
		{"global privilege", []string{
			"GRANT SELECT, EVENT, TRIGGER ON *.* TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, true, true},
		{"database privilege", []string{
			"GRANT SELECT, TRIGGER ON `probe`.* TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, true, true},
		{"another database", []string{
			"GRANT SELECT, TRIGGER ON `other`.* TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, false, true},
		{"all privileges on the database", []string{
			"GRANT ALL PRIVILEGES ON `probe`.* TO `u`@`%`",
		}, "probe", []string{"EVENT"}, true, true},
		{"all on MySQL 5.6 and 5.7 form", []string{
			"GRANT ALL PRIVILEGES ON *.* TO 'root'@'%' IDENTIFIED BY PASSWORD '*ABC' WITH GRANT OPTION",
		}, "probe", []string{"EVENT"}, true, true},
		{"one of several privileges", []string{
			"GRANT SELECT ON *.* TO `u`@`%`",
			"GRANT SHOW_ROUTINE ON *.* TO `u`@`%`",
		}, "probe", []string{"SHOW_ROUTINE", "EXECUTE"}, true, true},
		{"table level is not enough", []string{
			"GRANT TRIGGER ON `probe`.`t` TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, false, true},
		{"select on mysql.proc lists routines (5.7, MariaDB)", []string{
			"GRANT SELECT ON `probe`.* TO 'u'@'%'",
			"GRANT SELECT ON `mysql`.`proc` TO 'u'@'%'",
		}, "probe", []string{"EXECUTE", selectOnProcTable}, true, true},
		{"global select includes mysql.proc", []string{
			"GRANT SELECT ON *.* TO 'u'@'%'",
		}, "probe", []string{"EXECUTE", selectOnProcTable}, true, true},
		{"select on mysql.* includes mysql.proc", []string{
			"GRANT SELECT ON `mysql`.* TO 'u'@'%'",
		}, "probe", []string{"EXECUTE", selectOnProcTable}, true, true},
		{"select on another database does not", []string{
			"GRANT SELECT ON `probe`.* TO 'u'@'%'",
		}, "probe", []string{"EXECUTE", selectOnProcTable}, false, true},
		{"select on another table of mysql is not enough", []string{
			"GRANT SELECT ON `mysql`.`user` TO 'u'@'%'",
		}, "probe", []string{"EXECUTE", selectOnProcTable}, false, true},
		{"insert on mysql.proc is not enough", []string{
			"GRANT INSERT ON `mysql`.`proc` TO 'u'@'%'",
		}, "probe", []string{"EXECUTE", selectOnProcTable}, false, true},
		{"routine level is not enough", []string{
			"GRANT EXECUTE ON PROCEDURE `probe`.`p` TO `u`@`%`",
		}, "probe", []string{"EXECUTE"}, false, true},
		{"column privileges do not hide the rest", []string{
			"GRANT SELECT (`id`, `a`), INSERT (`id`) ON `probe`.`t` TO `u`@`%`",
			"GRANT TRIGGER ON `probe`.* TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, true, true},
		{"underscore in the pattern is a wildcard", []string{
			"GRANT TRIGGER ON `pr_be`.* TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, true, true},
		{"escaped underscore is literal", []string{
			"GRANT TRIGGER ON `pr\\_be`.* TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, false, true},
		{"percent in the pattern", []string{
			"GRANT TRIGGER ON `pro%`.* TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, true, true},
		{"doubled backtick in a name", []string{
			"GRANT TRIGGER ON `a``b`.* TO `u`@`%`",
		}, "a`b", []string{"TRIGGER"}, true, true},
		{"proxy line is not a scope", []string{
			"GRANT PROXY ON ''@'' TO 'u'@'%'",
			"GRANT SELECT ON `probe`.* TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, false, true},
		{"a role makes the privileges unknown", []string{
			"GRANT `r`@`%` TO `u`@`%`",
			"GRANT SELECT ON `probe`.* TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, false, false},
		{"unquoted names (TiDB)", []string{
			"GRANT SELECT,TRIGGER ON probe.* TO 'u'@'%'",
		}, "probe", []string{"TRIGGER"}, true, true},
		{"account name with a backslash-escaped quote", []string{
			"GRANT SELECT, TRIGGER ON `probe`.* TO 'o\\'brien'@'%'",
		}, "probe", []string{"TRIGGER"}, true, true},
		{"account name with a doubled quote", []string{
			"GRANT SELECT, TRIGGER ON `probe`.* TO 'o''brien'@'%'",
		}, "probe", []string{"TRIGGER"}, true, true},
		{"unreadable scope", []string{
			"GRANT SELECT ON ???? TO `u`@`%`",
		}, "probe", []string{"TRIGGER"}, false, false},
		{"no grants at all", nil, "probe", []string{"TRIGGER"}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, ok := parseGrants(c.lines)
			if ok != c.parsed {
				t.Fatalf("parseGrants ok = %v, want %v", ok, c.parsed)
			}
			if !ok {
				return
			}
			if got := g.covers(c.db, c.privs...); got != c.want {
				t.Errorf("covers(%q, %q) = %v, want %v", c.db, c.privs, got, c.want)
			}
		})
	}
}

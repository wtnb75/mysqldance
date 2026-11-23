package schemafile

import "testing"

func TestViewSecurityInvoker(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"stripped definer",
			"CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS select 1 AS `a`",
			"CREATE ALGORITHM=UNDEFINED SQL SECURITY INVOKER VIEW `v` AS select 1 AS `a`"},
		{"kept definer",
			"CREATE ALGORITHM=UNDEFINED DEFINER=`u`@`%` SQL SECURITY DEFINER VIEW `v` AS select 1 AS `a`",
			"CREATE ALGORITHM=UNDEFINED DEFINER=`u`@`%` SQL SECURITY INVOKER VIEW `v` AS select 1 AS `a`"},
		{"or replace",
			"CREATE OR REPLACE ALGORITHM=MERGE SQL SECURITY DEFINER VIEW `v` AS select 1 AS `a`",
			"CREATE OR REPLACE ALGORITHM=MERGE SQL SECURITY INVOKER VIEW `v` AS select 1 AS `a`"},
		{"already invoker",
			"CREATE ALGORITHM=UNDEFINED SQL SECURITY INVOKER VIEW `v` AS select 1 AS `a`",
			"CREATE ALGORITHM=UNDEFINED SQL SECURITY INVOKER VIEW `v` AS select 1 AS `a`"},
		{"text in the body is not touched",
			"CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS select 'SQL SECURITY DEFINER VIEW' AS `a`",
			"CREATE ALGORITHM=UNDEFINED SQL SECURITY INVOKER VIEW `v` AS select 'SQL SECURITY DEFINER VIEW' AS `a`"},
		{"text in the view name and body only",
			"CREATE ALGORITHM=UNDEFINED SQL SECURITY INVOKER VIEW `SQL SECURITY DEFINER` AS select 'SQL SECURITY DEFINER ' AS `a`",
			"CREATE ALGORITHM=UNDEFINED SQL SECURITY INVOKER VIEW `SQL SECURITY DEFINER` AS select 'SQL SECURITY DEFINER ' AS `a`"},
		{"definer user named like the clause",
			"CREATE ALGORITHM=UNDEFINED DEFINER=`SQL SECURITY DEFINER`@`h` SQL SECURITY DEFINER VIEW `v` AS select 1 AS `a`",
			"CREATE ALGORITHM=UNDEFINED DEFINER=`SQL SECURITY DEFINER`@`h` SQL SECURITY INVOKER VIEW `v` AS select 1 AS `a`"},
	}
	for _, tt := range tests {
		got, err := ViewSecurityInvoker(tt.in)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
		} else if got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
	}
}

func TestViewSecurityInvokerUnparsable(t *testing.T) {
	for _, in := range []string{
		"CREATE ALGORITHM=UNDEFINED DEFINER=role SQL SECURITY DEFINER VIEW `v` AS select 1",
		"CREATE VIEW `v` AS select 1",
		"",
	} {
		if got, err := ViewSecurityInvoker(in); err == nil {
			t.Errorf("%q: got %q, want an error", in, got)
		}
	}
}

func TestRoutineSecurityInvoker(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"no characteristics",
			"CREATE FUNCTION `f`() RETURNS int\nRETURN 1",
			"CREATE FUNCTION `f`() RETURNS int\n    SQL SECURITY INVOKER\nRETURN 1"},
		{"with characteristics",
			"CREATE FUNCTION `f`() RETURNS int\n    DETERMINISTIC\nRETURN 1",
			"CREATE FUNCTION `f`() RETURNS int\n    SQL SECURITY INVOKER\n    DETERMINISTIC\nRETURN 1"},
		{"procedure with a definer kept",
			"CREATE DEFINER=`u`@`%` PROCEDURE `p`(IN a int, OUT b int)\nBEGIN\n  SET b = a;\nEND",
			"CREATE DEFINER=`u`@`%` PROCEDURE `p`(IN a int, OUT b int)\n    SQL SECURITY INVOKER\nBEGIN\n  SET b = a;\nEND"},
		{"already invoker",
			"CREATE FUNCTION `f`() RETURNS int\n    DETERMINISTIC\n    SQL SECURITY INVOKER\nRETURN 1",
			"CREATE FUNCTION `f`() RETURNS int\n    DETERMINISTIC\n    SQL SECURITY INVOKER\nRETURN 1"},
		{"newline inside an ENUM type",
			"CREATE FUNCTION `f`(x enum('a\nb','c')) RETURNS int\nRETURN 1",
			"CREATE FUNCTION `f`(x enum('a\nb','c')) RETURNS int\n    SQL SECURITY INVOKER\nRETURN 1"},
		{"newline inside a returned ENUM type",
			"CREATE FUNCTION `f`() RETURNS enum('a\nb','c')\nRETURN 'a'",
			"CREATE FUNCTION `f`() RETURNS enum('a\nb','c')\n    SQL SECURITY INVOKER\nRETURN 'a'"},
		{"newline in the routine name",
			"CREATE FUNCTION `f\ng`() RETURNS int\nRETURN 1",
			"CREATE FUNCTION `f\ng`() RETURNS int\n    SQL SECURITY INVOKER\nRETURN 1"},
		{"newline between parameters",
			"CREATE PROCEDURE `p`(IN a int,\n IN b int)\nSELECT 1",
			"CREATE PROCEDURE `p`(IN a int,\n IN b int)\n    SQL SECURITY INVOKER\nSELECT 1"},
		{"text in the body is not an existing clause",
			"CREATE PROCEDURE `p`()\nSELECT '\n    SQL SECURITY INVOKER\n'",
			"CREATE PROCEDURE `p`()\n    SQL SECURITY INVOKER\nSELECT '\n    SQL SECURITY INVOKER\n'"},
		{"explicit definer line is replaced",
			"CREATE FUNCTION `f`() RETURNS int\n    SQL SECURITY DEFINER\n    DETERMINISTIC\nRETURN 1",
			"CREATE FUNCTION `f`() RETURNS int\n    SQL SECURITY INVOKER\n    DETERMINISTIC\nRETURN 1"},
		{"indented line inside a multi-line comment",
			"CREATE PROCEDURE `p`()\n    COMMENT 'x\n    SQL SECURITY INVOKER\n    '\nSELECT 1",
			"CREATE PROCEDURE `p`()\n    SQL SECURITY INVOKER\n    COMMENT 'x\n    SQL SECURITY INVOKER\n    '\nSELECT 1"},
		{"comment characteristic",
			"CREATE PROCEDURE `p`()\n    COMMENT 'SQL SECURITY INVOKER'\nSELECT 1",
			"CREATE PROCEDURE `p`()\n    SQL SECURITY INVOKER\n    COMMENT 'SQL SECURITY INVOKER'\nSELECT 1"},
	}
	for _, tt := range tests {
		got, err := RoutineSecurityInvoker(tt.in)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
	}
}

// A statement whose signature line cannot be found is an error, never output
// unchanged: the routine would keep running as its definer.
func TestRoutineSecurityInvokerUnparsable(t *testing.T) {
	for _, in := range []string{
		"CREATE FUNCTION `f`() RETURNS int RETURN 1",
		"CREATE FUNCTION `f`(x enum('a\nb) RETURNS int",
		"",
	} {
		if got, err := RoutineSecurityInvoker(in); err == nil {
			t.Errorf("%q: got %q, want an error", in, got)
		}
	}
}

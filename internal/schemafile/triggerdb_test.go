package schemafile

import (
	"strings"
	"testing"
)

func TestStripTriggerDB(t *testing.T) {
	const tail = " FOR EACH ROW SET NEW.s = 'qexp.t `qexp`.`t` ON qexp.t'"
	cases := []struct{ name, in, want string }{
		{"unqualified is kept", "CREATE TRIGGER `t` BEFORE INSERT ON `a`" + tail, "CREATE TRIGGER `t` BEFORE INSERT ON `a`" + tail},
		{"bare names", "CREATE TRIGGER qexp.trg BEFORE INSERT ON qexp.t" + tail, "CREATE TRIGGER trg BEFORE INSERT ON t" + tail},
		{"quoted names", "CREATE TRIGGER `qexp`.`trg` AFTER UPDATE ON `qexp`.`t`" + tail, "CREATE TRIGGER `trg` AFTER UPDATE ON `t`" + tail},
		{"mixed", "CREATE TRIGGER `qexp`.trg AFTER DELETE ON qexp.`t`" + tail, "CREATE TRIGGER trg AFTER DELETE ON `t`" + tail},
		{"only the table is qualified", "CREATE TRIGGER trg BEFORE INSERT ON qexp.t" + tail, "CREATE TRIGGER trg BEFORE INSERT ON t" + tail},
		{"case and whitespace kept", "create  trigger\n qexp.trg\tbefore insert on\n qexp.t for each row SET @a = 1", "create  trigger\n trg\tbefore insert on\n t for each row SET @a = 1"},
		{"backtick in a name", "CREATE TRIGGER `q``x`.`a``b` BEFORE INSERT ON `q``x`.`t`" + tail, "CREATE TRIGGER `a``b` BEFORE INSERT ON `t`" + tail},
		{"dot inside a quoted name", "CREATE TRIGGER `qexp`.`a.b` BEFORE INSERT ON `qexp`.`t.u`" + tail, "CREATE TRIGGER `a.b` BEFORE INSERT ON `t.u`" + tail},
		{"ANSI_QUOTES names", `CREATE TRIGGER "qexp"."trg" AFTER UPDATE ON "qexp"."t"` + tail, `CREATE TRIGGER "trg" AFTER UPDATE ON "t"` + tail},
		{"dollar and non-ASCII", "CREATE TRIGGER qexp.t$1 BEFORE INSERT ON qexp.表" + tail, "CREATE TRIGGER t$1 BEFORE INSERT ON 表" + tail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := "qexp"
			if strings.Contains(c.in, "`q``x`") {
				db = "q`x"
			}
			got, err := StripTriggerDB(c.in, db)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestStripTriggerDBCompareIgnoresCase(t *testing.T) {
	got, err := StripTriggerDB("CREATE TRIGGER QEXP.trg BEFORE INSERT ON qexp.t FOR EACH ROW SET @a = 1", "Qexp")
	if err != nil {
		t.Fatal(err)
	}
	if want := "CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET @a = 1"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripTriggerDBErrors(t *testing.T) {
	cases := []struct{ name, in, wantErr string }{
		{"other database", "CREATE TRIGGER other.trg BEFORE INSERT ON other.t FOR EACH ROW SET @a = 1", "other"},
		{"table in another database", "CREATE TRIGGER trg BEFORE INSERT ON other.t FOR EACH ROW SET @a = 1", "other"},
		{"if not exists", "CREATE TRIGGER IF NOT EXISTS qexp.trg BEFORE INSERT ON qexp.t FOR EACH ROW SET @a = 1", "cannot parse"},
		{"or replace", "CREATE OR REPLACE TRIGGER qexp.trg BEFORE INSERT ON qexp.t FOR EACH ROW SET @a = 1", "cannot parse"},
		{"spaces around the dot", "CREATE TRIGGER qexp . trg BEFORE INSERT ON qexp.t FOR EACH ROW SET @a = 1", "cannot parse"},
		{"three parts", "CREATE TRIGGER a.qexp.trg BEFORE INSERT ON qexp.t FOR EACH ROW SET @a = 1", "cannot parse"},
		{"unterminated quote", "CREATE TRIGGER `qexp.trg BEFORE INSERT ON qexp.t FOR EACH ROW SET @a = 1", "cannot parse"},
		{"no timing", "CREATE TRIGGER qexp.trg INSERT ON qexp.t FOR EACH ROW SET @a = 1", "cannot parse"},
		{"no ON", "CREATE TRIGGER qexp.trg BEFORE INSERT qexp.t FOR EACH ROW SET @a = 1", "cannot parse"},
		{"no FOR EACH ROW", "CREATE TRIGGER qexp.trg BEFORE INSERT ON qexp.t SET @a = 1", "cannot parse"},
		{"not a trigger", "CREATE VIEW v AS SELECT 1", "cannot parse"},
		{"empty", "", "cannot parse"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := StripTriggerDB(c.in, "qexp")
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("err = %v, want one containing %q", err, c.wantErr)
			}
		})
	}
}

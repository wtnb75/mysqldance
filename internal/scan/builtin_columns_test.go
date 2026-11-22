package scan

import (
	"reflect"
	"slices"
	"testing"
)

func columnsConfig(t *testing.T) *Config {
	t.Helper()
	return mustParse(t, "[extend]\nuseBuiltin = [\"pii-columns\"]\n")
}

// columnRules returns the ids of the rules that flag a non-empty cell of the
// column (path is DB.TABLE.COLUMN).
func columnRules(c *Config, path string) []string {
	var ids []string
	for _, m := range c.Scan(path, "x") {
		ids = append(ids, m.RuleID)
	}
	return ids
}

func TestPIIColumnRulesLoad(t *testing.T) {
	c := columnsConfig(t)
	var ids []string
	for _, r := range c.rules {
		ids = append(ids, r.id)
		if r.regex != nil || r.path == nil {
			t.Errorf("%s: a column rule must have a path and no regex", r.id)
		}
		if len(r.keywords) != 0 || len(r.allow) != 0 || r.validate != nil || r.entropy != 0 {
			t.Errorf("%s: a column rule must be a bare path", r.id)
		}
	}
	want := []string{
		"pii-column-person-name", "pii-column-email", "pii-column-phone", "pii-column-address",
		"pii-column-postal", "pii-column-birth", "pii-column-gov-id", "pii-column-freetext",
		"pii-column-name-generic",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("rules = %v, want %v", ids, want)
	}
}

func TestPIIColumnRules(t *testing.T) {
	cases := []struct {
		rule string
		yes  []string // paths this rule flags
		no   []string // paths this rule does not flag
	}{
		{"pii-column-person-name",
			[]string{"app.users.first_name", "app.users.firstName", "a.b.LAST_NAME", "a.b.lastname", "a.b.full-name",
				"a.b.family_name", "a.b.given_name", "a.b.surname", "a.b.user_surname", "shop.customers.氏名",
				"a.b.姓名", "a.b.顧客名前", "a.b.フリガナ", "a.b.ふりがな", "a.b.furigana", "a.b.name_kana", "a.b.kana"},
			[]string{"a.b.name", "a.b.product_name", "a.b.table_name", "a.b.username", "a.b.file_name", "a.b.kanalog", "a.b.id"}},
		{"pii-column-email",
			[]string{"a.b.email", "a.b.Email", "a.b.user_email", "a.b.e_mail", "a.b.e-mail", "a.b.mail", "a.b.mail_to",
				"a.b.メールアドレス", "a.b.contact_email_1"},
			[]string{"a.b.female", "a.b.mailbox", "a.b.gmailx", "a.b.id", "a.b.name"}},
		{"pii-column-phone",
			[]string{"a.b.tel", "a.b.user_tel", "a.b.tel_no", "a.b.TEL", "a.b.phone", "a.b.phone_number", "a.b.mobile",
				"a.b.fax", "a.b.telephone", "a.b.cellphone", "a.b.電話番号", "a.b.携帯", "a.b.home-tel", "a.b.tel1", "a.b.phone2", "a.b.fax1"},
			[]string{"a.b.hotel", "a.b.total", "a.b.hotel_id", "a.b.telegram", "a.b.microphone", "a.b.faxless", "a.b.mobiles", "a.b.tel1x"}},
		{"pii-column-address",
			[]string{"a.b.address", "a.b.street_address", "a.b.addr", "a.b.addr1", "a.b.street", "a.b.住所", "a.b.番地", "a.b.Address_Line_2", "a.b.billing_addr1", "a.b.street1"},
			[]string{"a.b.add", "a.b.adder", "a.b.streets", "a.b.id"}},
		{"pii-column-postal",
			[]string{"a.b.zip", "a.b.zip_code", "a.b.zipcode", "a.b.postal_code", "a.b.postcode", "a.b.post_code", "a.b.郵便番号", "a.b.billing_zip", "a.b.zip1"},
			[]string{"a.b.zipper", "a.b.post", "a.b.postage", "a.b.code"}},
		{"pii-column-birth",
			[]string{"a.b.birthday", "a.b.birth_date", "a.b.date_of_birth", "a.b.dob", "a.b.user_dob", "a.b.生年月日", "a.b.誕生日", "a.b.Birthdate"},
			[]string{"a.b.dobby", "a.b.date", "a.b.id"}},
		{"pii-column-gov-id",
			[]string{"a.b.my_number", "a.b.mynumber", "a.b.ssn", "a.b.user_ssn", "a.b.passport", "a.b.passport_no", "a.b.マイナンバー",
				"a.b.旅券番号", "a.b.パスポート", "a.b.card_number", "a.b.card_no", "a.b.credit_card_number", "a.b.cardnumber"},
			[]string{"a.b.lssn", "a.b.card_note", "a.b.discard_no", "a.b.card", "a.b.number", "a.b.card_type"}},
		{"pii-column-freetext",
			[]string{"a.b.comment", "a.b.comments", "a.b.user_comment", "a.b.note", "a.b.notes", "a.b.memo", "a.b.remark", "a.b.remarks",
				"a.b.備考", "a.b.メモ", "a.b.コメント", "a.b.admin-memo", "a.b.メモ帳", "a.b.メモ1"},
			[]string{"a.b.description", "a.b.body", "a.b.commenter_id", "a.b.notebook", "a.b.memory", "a.b.denote", "a.b.メモリ使用量", "a.b.メモリ"}},
		{"pii-column-name-generic",
			[]string{"products.name", "a.b.NAME", "x.y.Name"},
			[]string{"a.b.names", "a.b.product_name", "a.b.table_name", "a.b.name2", "a.b.first_name", "name.b.id", "a.name.id"}},
	}
	c := columnsConfig(t)
	for _, tc := range cases {
		t.Run(tc.rule, func(t *testing.T) {
			for _, p := range tc.yes {
				if got := columnRules(c, p); !slices.Contains(got, tc.rule) {
					t.Errorf("%s: rules = %v, want %s", p, got, tc.rule)
				}
			}
			for _, p := range tc.no {
				if got := columnRules(c, p); slices.Contains(got, tc.rule) {
					t.Errorf("%s: flagged by %s, want not", p, tc.rule)
				}
			}
		})
	}
}

// Columns that carry no personal data are flagged by nothing, and the ones the
// spec names flag exactly the one rule it names.
func TestPIIColumnExactRules(t *testing.T) {
	c := columnsConfig(t)
	for path, want := range map[string][]string{
		"users.first_name":      {"pii-column-person-name"},
		"customers.氏名":          {"pii-column-person-name"},
		"a.b.user_tel":          {"pii-column-phone"},
		"t.birthday":            {"pii-column-birth"},
		"products.name":         {"pii-column-name-generic"},
		"a.b.email":             {"pii-column-email"},
		"a.b.mail_address":      {"pii-column-email", "pii-column-address"},
		"t.hotel":               nil,
		"t.total":               nil,
		"products.product_name": nil,
		"t.description":         nil,
		"t.body":                nil,
		"t.id":                  nil,
		"t.table_name":          nil,
	} {
		if got := columnRules(c, path); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: rules = %v, want %v", path, got, want)
		}
	}
}

// Only the column name is looked at: a database or table named like a column
// does not flag every column in it.
func TestPIIColumnRulesLookAtTheColumnOnly(t *testing.T) {
	c := columnsConfig(t)
	for _, p := range []string{"email.users.id", "app.address.id", "tel.tel_log.id", "name.name.id", "app.comment_log.id"} {
		if got := columnRules(c, p); got != nil {
			t.Errorf("%s: rules = %v, want none", p, got)
		}
	}
}

// A cell that is NULL or empty is never a finding, so an empty column is not
// reported.
func TestPIIColumnRulesIgnoreEmptyCells(t *testing.T) {
	c := columnsConfig(t)
	if got := c.Scan("a.users.email", ""); got != nil {
		t.Errorf("an empty cell was flagged: %v", got)
	}
	m := c.Scan("a.users.email", "hello")
	if len(m) != 1 || m[0].Start != 0 || m[0].Length != len("hello") {
		t.Errorf("findings = %+v, want the whole value", m)
	}
}

func TestPIIColumnsWithOptions(t *testing.T) {
	// Switched off by id, and the value rules can be used next to them.
	c := mustParse(t, "[extend]\nuseBuiltin = [\"pii\", \"pii-columns\"]\ndisabledRules = [\"pii-column-name-generic\", \"pii-ipv4\"]\n")
	if got := columnRules(c, "products.name"); got != nil {
		t.Errorf("a disabled rule flagged: %v", got)
	}
	if got := columnRules(c, "a.users.email"); !slices.Contains(got, "pii-column-email") {
		t.Errorf("rules = %v", got)
	}
	// A rule of the user's own with the same id takes over.
	c = mustParse(t, `
[extend]
useBuiltin = ["pii-columns"]
[[rules]]
id = "pii-column-phone"
path = '''(?i)\.contact_no$'''
`)
	if got := columnRules(c, "a.b.user_tel"); slices.Contains(got, "pii-column-phone") {
		t.Errorf("the built-in rule was kept: %v", got)
	}
	if got := columnRules(c, "a.b.contact_no"); !reflect.DeepEqual(got, []string{"pii-column-phone"}) {
		t.Errorf("rules = %v", got)
	}
}

// The same cells give the same findings in the same order every time.
func TestPIIColumnsAreDeterministic(t *testing.T) {
	c := mustParse(t, "[extend]\nuseBuiltin = [\"pii-columns\", \"pii\"]\n")
	first := c.Scan("a.users.mail_address", "taro@example.com")
	if len(first) < 3 {
		t.Fatalf("findings = %+v", first)
	}
	for range 5 {
		if got := c.Scan("a.users.mail_address", "taro@example.com"); !reflect.DeepEqual(got, first) {
			t.Fatalf("findings changed: %+v vs %+v", got, first)
		}
	}
}

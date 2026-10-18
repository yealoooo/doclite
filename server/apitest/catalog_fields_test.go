package apitest

import (
	"slices"
	"strings"
	"testing"
)

var testItemFields = []any{
	map[string]any{"key": "author", "label": "责任者", "type": "text", "required": true, "max_length": 10},
	map[string]any{"key": "doc_no", "label": "文号", "type": "text", "max_length": 5},
	map[string]any{"key": "doc_date", "label": "日期", "type": "date", "required": true},
	map[string]any{"key": "secret", "label": "密级", "type": "enum", "options": []any{"公开", "内部", "秘密"}, "default": "内部"},
	map[string]any{"key": "remark", "label": "备注", "type": "text"},
}

// TestItemFields：著录校验（未锁定只查格式、details.fields 一次列全）、默认值、catalog-settings、改设置后的旧值规则、
// 锁定时的必填检查。
func TestItemFields(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "FD")
	_, _, _, _, org := c.mkChain()
	c.setSettings(map[string]any{"item_fields": testItemFields})

	// catalog-settings：补上默认值的著录项、模板用到的层级、编码规则。
	var st struct {
		Template  string           `json:"archive_no_template"`
		Error     *string          `json:"archive_no_template_error"`
		Levels    []string         `json:"archive_no_levels"`
		ItemField []map[string]any `json:"item_fields"`
		Lock      bool             `json:"lock_requires_approved"`
		Export    bool             `json:"require_locked_item"`
		Types     []string         `json:"category_types"`
		CodeMax   int              `json:"category_code_max_chars"`
		Forbidden []string         `json:"category_code_forbidden_chars"`
		NameMax   int              `json:"category_name_max_chars"`
		Dates     []string         `json:"date_formats"`
	}
	none := c.As("none") // 能进项目即可：没有菜单的人授权后也能读
	c.GrantProject(c.pid, c.PersonaID("none"))
	none.Get("/api/projects/" + c.pid.String() + "/catalog-settings").Expect(200).JSON(&st)
	remark := st.ItemField[4]
	if st.Error != nil || !slices.Equal(st.Levels, []string{"fonds", "category", "year", "retention", "org"}) || len(st.ItemField) != 5 ||
		remark["max_length"] != float64(500) || remark["required"] != false || len(remark["options"].([]any)) != 0 || remark["default"] != nil ||
		st.ItemField[2]["max_length"] != nil || !st.Lock || !st.Export || len(st.Types) != 6 || st.CodeMax != 32 || st.NameMax != 100 ||
		!slices.Contains(st.Forbidden, "-") || !slices.Contains(st.Forbidden, "·") || !slices.Contains(st.Forbidden, "{") ||
		!slices.Equal(st.Dates, []string{"YYYY", "YYYY-MM", "YYYY-MM-DD"}) {
		t.Fatalf("catalog-settings：%+v", st)
	}

	// 归档：题名去首尾空白，著录项用默认值；之后 PATCH 按 key 合并（null 清空）；未锁定不查必填。
	it := c.mkItem(org.ID, map[string]any{"title": " 题名 ", "extra_fields": map[string]any{"author": " 张三 "}})
	if it.Title != "题名" || it.ExtraFields["author"] != "张三" || it.ExtraFields["secret"] != "内部" || len(it.ExtraFields) != 2 {
		t.Fatalf("新建：%+v", it)
	}
	if noDefault := c.mkItem(org.ID, map[string]any{"extra_fields": map[string]any{"secret": nil}}); len(noDefault.ExtraFields) != 0 {
		t.Fatalf("明确清空就不填默认值：%v", noDefault.ExtraFields)
	}

	// 草稿只查格式（表驱动）：keys 为空表示通过。
	cases := []struct {
		name   string
		title  any
		fields map[string]any
		keys   []string
		msg    string
	}{
		{"题名超长", strings.Repeat("题", 1001), nil, []string{"title"}, "题名最长 1000 个字符"},
		{"题名换行", "第一行\n第二行", nil, []string{"title"}, "题名不能有控制字符"},
		{"题名 1000 字", strings.Repeat("题", 1000), nil, nil, ""},
		{"文本超长", nil, map[string]any{"doc_no": "123456"}, []string{"doc_no"}, "文号最长 5 个字符"},
		{"文本控制字符", nil, map[string]any{"remark": "a\x07b"}, []string{"remark"}, "备注不能有控制字符"},
		{"文本换行可以", nil, map[string]any{"remark": "第一行\n第二行"}, nil, ""},
		{"默认最长 500", nil, map[string]any{"remark": strings.Repeat("字", 501)}, []string{"remark"}, "备注最长 500 个字符"},
		{"日期不存在", nil, map[string]any{"doc_date": "2025-02-30"}, []string{"doc_date"}, "日期不是合法的日期"},
		{"日期格式", nil, map[string]any{"doc_date": "2025/03"}, []string{"doc_date"}, "日期不是合法的日期"},
		{"闰年", nil, map[string]any{"doc_date": "2024-02-29"}, nil, ""},
		{"年月", nil, map[string]any{"doc_date": "2025-03"}, nil, ""},
		{"不在选项里", nil, map[string]any{"secret": "绝密"}, []string{"secret"}, "密级只能从选项里选（公开、内部、秘密）"},
		{"不是字符串", nil, map[string]any{"author": 5}, []string{"author"}, "责任者要是字符串"},
		{"设置里没有", nil, map[string]any{"nope": "x"}, []string{"nope"}, "项目设置里没有著录项“nope”"},
		{"设置里没有的可以清空", nil, map[string]any{"nope": nil}, nil, ""},
		{"空串清空", nil, map[string]any{"doc_no": " "}, nil, ""},
		{"一次列全，按题名、设置里的顺序、未知的在后", "a\tb", map[string]any{"nope": "x", "secret": "x", "doc_no": "123456"},
			[]string{"title", "doc_no", "secret", "nope"}, "著录内容不对：题名不能有控制字符"},
	}
	for _, tc := range cases {
		before := c.item(it.ID)
		body := map[string]any{"row_version": before.RowVersion}
		if tc.title != nil {
			body["title"] = tc.title
		}
		if tc.fields != nil {
			body["extra_fields"] = tc.fields
		}
		res := c.admin.Patch("/api/archive-items/"+it.ID.String(), body)
		if tc.keys == nil {
			if res.Status != 200 {
				t.Errorf("%s：%d %s", tc.name, res.Status, res.Body)
			}
			continue
		}
		if res.Status != 400 || res.Code() != "invalid_input" {
			t.Errorf("%s：%d %s", tc.name, res.Status, res.Body)
			continue
		}
		e := errOf(res)
		var keys []string
		for _, f := range e.Details.Fields {
			keys = append(keys, f.Key)
		}
		if !slices.Equal(keys, tc.keys) || !strings.Contains(e.Message+e.Details.Fields[0].Message, tc.msg) {
			t.Errorf("%s：%v %s %v", tc.name, keys, e.Message, e.Details.Fields)
		}
		if after := c.item(it.ID); after.RowVersion != before.RowVersion {
			t.Errorf("%s：校验不过也改了条目", tc.name)
		}
	}
	got := c.item(it.ID)
	if got.ExtraFields["doc_no"] != nil || got.ExtraFields["doc_date"] != "2025-03" || got.Title != strings.Repeat("题", 1000) {
		t.Fatalf("合并后的著录：%+v", got.ExtraFields)
	}

	// 锁定时查必填（含题名）。
	empty := c.mkItem(org.ID, nil)
	c.patchItem(empty.ID, map[string]any{"title": ""}).Expect(200)
	e := errOf(c.act(empty.ID, "lock").ExpectError(400, "invalid_input"))
	if len(e.Details.Fields) != 3 || e.Details.Fields[0].Message != "题名不能为空" || e.Details.Fields[1].Message != "责任者不能为空" ||
		e.Details.Fields[2].Message != "日期不能为空" {
		t.Fatalf("必填：%+v", e)
	}

	// 旧值规则：改设置后（删掉选项、改短、删掉著录项）旧值不改就能保存，详情用 field_issues 标出，不悄悄丢掉。
	old := c.mkItem(org.ID, map[string]any{"title": "旧值", "extra_fields": map[string]any{
		"author": "李四", "doc_date": "2025", "secret": "秘密", "doc_no": "12345", "remark": "旧备注"}})
	fields := []any{testItemFields[0], testItemFields[1], testItemFields[2], testItemFields[3]}
	fields[1] = map[string]any{"key": "doc_no", "label": "文号", "type": "text", "max_length": 3}
	fields[3] = map[string]any{"key": "secret", "label": "密级", "type": "enum", "options": []any{"公开", "内部"}}
	c.setSettings(map[string]any{"item_fields": fields})
	old = c.item(old.ID)
	var issueKeys []string
	for _, is := range old.FieldIssues {
		issueKeys = append(issueKeys, is.Key)
	}
	if !slices.Equal(issueKeys, []string{"doc_no", "secret", "remark"}) || !strings.Contains(old.FieldIssues[1].Message, "原值“秘密”") ||
		!strings.Contains(old.FieldIssues[2].Message, "已经没有著录项“remark”") || old.ExtraFields["remark"] != "旧备注" {
		t.Fatalf("field_issues：%+v", old.FieldIssues)
	}
	c.patchItem(old.ID, map[string]any{"title": "旧值 2"}).Expect(200)
	c.patchItem(old.ID, map[string]any{"extra_fields": map[string]any{"secret": "秘密", "doc_no": "12345", "remark": "旧备注"}}).Expect(200)
	c.patchItem(old.ID, map[string]any{"extra_fields": map[string]any{"remark": "新备注"}}).ExpectError(400, "invalid_input")
	c.patchItem(old.ID, map[string]any{"extra_fields": map[string]any{"doc_no": "1234"}}).ExpectError(400, "invalid_input")
	if got := c.item(old.ID); got.ExtraFields["secret"] != "秘密" || got.ExtraFields["remark"] != "旧备注" || got.Title != "旧值 2" {
		t.Fatalf("旧值没保留：%+v", got)
	}
	// 锁定：现有的值要合现在的设置（设置里已经没有的不管）。
	e = errOf(c.act(old.ID, "lock").ExpectError(400, "invalid_input"))
	if len(e.Details.Fields) != 2 || e.Details.Fields[0].Key != "doc_no" || e.Details.Fields[1].Key != "secret" {
		t.Fatalf("锁定时的旧值：%+v", e.Details.Fields)
	}
	c.patchItem(old.ID, map[string]any{"extra_fields": map[string]any{"secret": "内部", "doc_no": "123"}}).Expect(200)
	c.act(old.ID, "lock").Expect(200)
	// 解锁后（未锁定）改著录只查格式：必填空着也能存，锁定时再查。
	c.act(old.ID, "unlock").Expect(200)
	c.patchItem(old.ID, map[string]any{"extra_fields": map[string]any{"author": nil}}).Expect(200)
	if got := Decode[itemView](c.patchItem(old.ID, map[string]any{"extra_fields": map[string]any{"doc_no": "12"}}).Expect(200)); got.Status != "draft" {
		t.Fatalf("未锁定的改著录：%s", got.Status)
	}
	upd := QueryValue[map[string]any](c.Env, `SELECT detail FROM audit_logs WHERE target_id = $1 AND action = 'update' ORDER BY id DESC LIMIT 1`, old.ID)
	if upd["before"].(map[string]any)["extra_fields"].(map[string]any)["doc_no"] != "123" ||
		len(upd["after"].(map[string]any)["extra_fields"].(map[string]any)) != 1 {
		t.Fatalf("update 审计只放变了的 key：%v", upd)
	}
}

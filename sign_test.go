package main

import "testing"

// 用文档「签名机制」章节的官方示例验证签名算法
// access key ID: testid, secret: testsecret
// 文档给出的签名结果: kRA2cnpJVacIhDMzXnoNZG9tDCI=  (URL编码后 kRA2cnpJVacIhDMzXnoNZG9tDCI%3D)
func TestSignDocExample(t *testing.T) {
	params := map[string]string{
		"AccessKeyId":      "testid",
		"Action":           "CreateUser",
		"Format":           "JSON",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   "6a6e0ca6-4557-11e5-86a2-b8e8563dc8d2",
		"SignatureVersion": "1.0",
		"Timestamp":        "2015-08-18T03:15:45Z",
		"UserName":         "test",
		"Version":          "2015-05-01",
	}
	got := sign("GET", params, "testsecret")
	want := "kRA2cnpJVacIhDMzXnoNZG9tDCI="
	if got != want {
		t.Fatalf("签名结果不一致: got=%q want=%q", got, want)
	}

	// URL 编码后的签名值也应与文档一致
	if pe := percentEncode(got); pe != "kRA2cnpJVacIhDMzXnoNZG9tDCI%3D" {
		t.Fatalf("签名URL编码不一致: got=%q", pe)
	}
}

func TestPercentEncode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abcXYZ012-_.~", "abcXYZ012-_.~"},
		{"a b", "a%20b"},
		{"2015-08-18T03:15:45Z", "2015-08-18T03%3A15%3A45Z"},
		{"密码@", "%E5%AF%86%E7%A0%81%40"},
	}
	for _, c := range cases {
		if got := percentEncode(c.in); got != c.want {
			t.Errorf("percentEncode(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestToInt(t *testing.T) {
	if toInt(float64(40)) != 40 {
		t.Error("float64 转换失败")
	}
	if toInt("40") != 40 {
		t.Error("string 转换失败")
	}
	if toInt(float64(2.0)) != 2 {
		t.Error("float64 2.0 转换失败")
	}
}

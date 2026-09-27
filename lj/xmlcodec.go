package lj

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

type rawValue struct {
	Array  *rawArray  `xml:"array"`
	Struct *rawStruct `xml:"struct"`
	String *string    `xml:"string"`
	Int    *string    `xml:"int"`
	I4     *string    `xml:"i4"`
	Bool   *string    `xml:"boolean"`
	Double *string    `xml:"double"`
	Base64 *string    `xml:"base64"`
	Date   *string    `xml:"dateTime.iso8601"`
	Char   string     `xml:",chardata"`
}

type rawArray struct {
	Values []rawValue `xml:"data>value"`
}

type rawStruct struct {
	Members []rawMember `xml:"member"`
}

type rawMember struct {
	Name  string   `xml:"name"`
	Value rawValue `xml:"value"`
}

type methodResponse struct {
	Params []struct {
		Value rawValue `xml:"value"`
	} `xml:"params>param"`
	Fault *struct {
		Value rawValue `xml:"value"`
	} `xml:"fault"`
}

func (v rawValue) goValue() (any, error) {
	switch {
	case v.Array != nil:
		out := make([]any, 0, len(v.Array.Values))
		for _, item := range v.Array.Values {
			g, err := item.goValue()
			if err != nil {
				return nil, err
			}
			out = append(out, g)
		}
		return out, nil
	case v.Struct != nil:
		out := make(map[string]any, len(v.Struct.Members))
		for _, m := range v.Struct.Members {
			g, err := m.Value.goValue()
			if err != nil {
				return nil, err
			}
			out[m.Name] = g
		}
		return out, nil
	case v.Base64 != nil:
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*v.Base64))
		if err != nil {
			return *v.Base64, nil
		}
		return string(b), nil
	case v.String != nil:
		return *v.String, nil
	case v.Int != nil:
		return parseXMLInt(*v.Int)
	case v.I4 != nil:
		return parseXMLInt(*v.I4)
	case v.Bool != nil:
		s := strings.TrimSpace(*v.Bool)
		return s == "1" || strings.EqualFold(s, "true"), nil
	case v.Double != nil:
		return strconv.ParseFloat(strings.TrimSpace(*v.Double), 64)
	case v.Date != nil:
		return *v.Date, nil
	default:
		return strings.TrimSpace(v.Char), nil
	}
}

func parseXMLInt(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

func decodeResponse(data []byte) (any, error) {
	var resp methodResponse
	if err := xml.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("lj: xmlrpc decode: %w", err)
	}
	if resp.Fault != nil {
		g, err := resp.Fault.Value.goValue()
		if err != nil {
			return nil, err
		}
		m := asMap(g)
		return nil, &FaultError{Code: int(asInt(m["faultCode"])), Message: asString(m["faultString"])}
	}
	if len(resp.Params) == 0 {
		return map[string]any{}, nil
	}
	return resp.Params[0].Value.goValue()
}

func encodeCall(method string, params map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	buf.WriteString(`<methodCall><methodName>`)
	if err := xml.EscapeText(&buf, []byte(method)); err != nil {
		return nil, err
	}
	buf.WriteString(`</methodName><params><param>`)
	if err := writeValue(&buf, params); err != nil {
		return nil, err
	}
	buf.WriteString(`</param></params></methodCall>`)
	return buf.Bytes(), nil
}

func writeValue(buf *bytes.Buffer, v any) error {
	buf.WriteString(`<value>`)
	switch t := v.(type) {
	case nil:
		buf.WriteString(`<string></string>`)
	case string:
		buf.WriteString(`<string>`)
		if err := xml.EscapeText(buf, []byte(t)); err != nil {
			return err
		}
		buf.WriteString(`</string>`)
	case int:
		fmt.Fprintf(buf, `<int>%d</int>`, t)
	case int32:
		fmt.Fprintf(buf, `<int>%d</int>`, t)
	case int64:
		fmt.Fprintf(buf, `<int>%d</int>`, t)
	case uint32:
		fmt.Fprintf(buf, `<int>%d</int>`, t)
	case uint64:
		fmt.Fprintf(buf, `<int>%d</int>`, t)
	case bool:
		if t {
			buf.WriteString(`<boolean>1</boolean>`)
		} else {
			buf.WriteString(`<boolean>0</boolean>`)
		}
	case float64:
		fmt.Fprintf(buf, `<double>%g</double>`, t)
	case map[string]any:
		buf.WriteString(`<struct>`)
		for k, val := range t {
			buf.WriteString(`<member><name>`)
			if err := xml.EscapeText(buf, []byte(k)); err != nil {
				return err
			}
			buf.WriteString(`</name>`)
			if err := writeValue(buf, val); err != nil {
				return err
			}
			buf.WriteString(`</member>`)
		}
		buf.WriteString(`</struct>`)
	case []any:
		buf.WriteString(`<array><data>`)
		for _, val := range t {
			if err := writeValue(buf, val); err != nil {
				return err
			}
		}
		buf.WriteString(`</data></array>`)
	default:
		return fmt.Errorf("lj: unsupported xmlrpc type %T", v)
	}
	buf.WriteString(`</value>`)
	return nil
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func asInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case uint32:
		return int64(t)
	case uint64:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	default:
		return 0
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func classifyFault(f *FaultError) error {
	if f == nil {
		return &AuthError{Reason: "rejected"}
	}
	m := strings.ToLower(f.Message)
	switch {
	case f.Code == 209:
		return &LimitError{Param: f.Message}
	case strings.Contains(m, "password"),
		strings.Contains(m, "auth"),
		strings.Contains(m, "challenge"),
		strings.Contains(m, "session"),
		strings.Contains(m, "username"):
		return &AuthError{Reason: "rejected"}
	default:
		return f
	}
}

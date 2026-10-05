package tja

import "testing"

func TestChartModeAndCourses(t *testing.T) {
	const header = "TITLE:Test\nBPM:120\nWAVE:music.ogg\nCOURSE:Oni\nLEVEL:5\n"
	const single = "#START\n1000,\n#END\n"
	const p1 = "#START P1\n1000,\n#END\n"
	const p2 = "#START P2\n2000,\n#END\n"
	for _, tc := range []struct {
		name, body string
		single     bool
		courses    []string
		errorCode  string
	}{
		{"implicit single", single, true, []string{"Oni"}, ""},
		{"explicit single", "STYLE:0\n" + single, true, []string{"Oni"}, ""},
		{"double players", "STYLE:Double\n" + p1 + p2, false, []string{"Oni_1p", "Oni_2p"}, ""},
		{"implicit double", p2 + p1, false, []string{"Oni_2p", "Oni_1p"}, ""},
		{"duet alias", "STYLE:Duet\n" + p1 + p2, false, []string{"Oni_1p", "Oni_2p"}, ""},
		{"no side", "STYLE:Double\n" + single, false, nil, "TJA_PLAYER_REQUIRED"},
		{"mixed", single + p1, false, nil, "TJA_MODE_MIXED"},
		{"mixed other course", p1 + "COURSE:Hard\nLEVEL:4\n" + single, false, nil, "TJA_MODE_MIXED"},
		{"duplicate single", single + single, false, nil, "TJA_DIFFICULTY_DUPLICATE"},
		{"duplicate side", p1 + p1, false, nil, "TJA_DIFFICULTY_DUPLICATE"},
		{"invalid style", "STYLE:Unknown\n" + single, false, nil, "TJA_STRUCTURE_INVALID"},
		{"style in block", "#START\nSTYLE:Double\n1000,\n#END\n", false, nil, "TJA_STRUCTURE_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, issue := Parse([]byte(header+tc.body), "utf-8", "music.ogg")
			if tc.errorCode != "" {
				if issue == nil || issue.Code != tc.errorCode || issue.Line == 0 {
					t.Fatalf("expected %s, got %v", tc.errorCode, issue)
				}
				return
			}
			if issue != nil || m.IsSingle != tc.single || len(m.Difficulties) != len(tc.courses) {
				t.Fatalf("%+v %v", m, issue)
			}
			for i, d := range m.Difficulties {
				if d.Course != tc.courses[i] {
					t.Fatal(m.Difficulties)
				}
			}
		})
	}
}

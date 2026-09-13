package tja

import "testing"

func TestCloudScoreEligibility(t *testing.T) {
	const header = "TITLE:Test\nBPM:120\nWAVE:music.ogg\nCOURSE:Oni\nLEVEL:5\n"
	const single = "#START\n1000,\n#END\n"
	const p1 = "#START P1\n1000,\n#END\n"
	const p2 = "#START P2\n2000,\n#END\n"
	for _, tc := range []struct {
		name, body string
		eligible   []bool
	}{
		{"implicit single", single, []bool{true}},
		{"explicit single", "STYLE:SINGLE\n" + single, []bool{true}},
		{"double without player", "STYLE:Double\n" + single, []bool{false}},
		{"double players", "STYLE:DOUBLE\n" + p1 + p2, []bool{false, false}},
		{"players without style", p1 + p2 + single, []bool{false, false, true}},
		{"single cannot override player", "STYLE:Single\n" + p2, []bool{false}},
		{"mixed styles", single + "STYLE:Double\n" + p1 + p2 + "COURSE:Easy\nLEVEL:3\nSTYLE:Single\n" + single, []bool{true, false, false, true}},
		{"numeric aliases", "STYLE:1\n" + single + "STYLE:0\n" + single, []bool{false, true}},
		{"ESE duet alias", "STYLE:Duet\n" + p1 + p2, []bool{false, false}},
		{"new course defaults single", "STYLE:Double\n" + p1 + "COURSE:Hard\nLEVEL:4\n" + single, []bool{false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, issue := Parse([]byte(header+tc.body), "utf-8", "music.ogg")
			if issue != nil {
				t.Fatal(issue)
			}
			if len(m.Difficulties) != len(tc.eligible) {
				t.Fatal("lost difficulty blocks")
			}
			for i, d := range m.Difficulties {
				style := "Double"
				if tc.eligible[i] {
					style = "Single"
				}
				if d.CloudScoreEligible != tc.eligible[i] || d.Style != style || d.BlockIndex != i {
					t.Fatalf("block %d: %+v", i, d)
				}
			}
		})
	}
	for _, body := range []string{"STYLE:Unknown\n" + single, "#START\nSTYLE:Double\n1000,\n#END\n"} {
		_, issue := Parse([]byte(header+body), "utf-8", "music.ogg")
		if issue == nil || issue.Code != "TJA_STRUCTURE_INVALID" || issue.Line == 0 {
			t.Fatalf("expected located STYLE error: %v", issue)
		}
	}
}

func TestRejectDuplicateSingleDifficulty(t *testing.T) {
	const header = "TITLE:Test\nBPM:120\nWAVE:music.ogg\nCOURSE:Oni\nLEVEL:5\n"
	const single = "#START\n1000,\n#END\n"
	for _, middle := range []string{"", "COURSE:oni\nLEVEL:6\n", "COURSE:3\nLEVEL:5\n", "STYLE:Double\n#START P1\n2000,\n#END\nSTYLE:Single\n"} {
		_, issue := Parse([]byte(header+single+middle+single), "utf-8", "music.ogg")
		if issue == nil || issue.Code != "TJA_DIFFICULTY_DUPLICATE" || issue.Line <= 6 {
			t.Fatalf("duplicate accepted or no location: %v", issue)
		}
	}
}

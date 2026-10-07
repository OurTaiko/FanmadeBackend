package tja

import "testing"

func TestBranchingIsReportedPerBlock(t *testing.T) {
	text := "TITLE:Branch\nBPM:120\nWAVE:a.ogg\n" +
		"COURSE:Hard\nLEVEL:6\n#START\n1000,\n#END\n" +
		"COURSE:Oni\nLEVEL:9\n#START\n1000,\n#BRANCHSTART p,80,95\n#N\n1000,\n#E\n2000,\n#M\n3000,\n#BRANCHEND\n#END\n" +
		"COURSE:Edit\nLEVEL:10\n#START\n#branchstart r,5,10 // lower case still counts\n#N\n1,\n#BRANCHEND\n#END\n"
	m, issue := Parse([]byte(text), "utf-8", "a.ogg")
	if issue != nil {
		t.Fatal(issue)
	}
	want := map[string]bool{"Hard": false, "Oni": true, "Edit": true}
	if len(m.Difficulties) != len(want) {
		t.Fatalf("got %d difficulties", len(m.Difficulties))
	}
	for _, d := range m.Difficulties {
		if d.Branching != want[d.Course] {
			t.Fatalf("%s branching = %v", d.Course, d.Branching)
		}
	}

	double := "TITLE:Double\nBPM:120\nWAVE:a.ogg\nCOURSE:Oni\nLEVEL:9\nSTYLE:Double\n" +
		"#START P1\n1000,\n#END\n#START P2\n#BRANCHSTART p,0,0\n#N\n1,\n#BRANCHEND\n#END\n"
	m, issue = Parse([]byte(double), "utf-8", "a.ogg")
	if issue != nil {
		t.Fatal(issue)
	}
	if m.Difficulties[0].Branching || !m.Difficulties[1].Branching {
		t.Fatalf("P1/P2 branching = %v/%v", m.Difficulties[0].Branching, m.Difficulties[1].Branching)
	}
}

package storage

import "testing"

func TestRefuseNewerDatabase(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec("INSERT INTO schema_migrations VALUES(2)"); e != nil {
		t.Fatal(e)
	}
	s.Close()
	if s, e = Open(dir); e == nil {
		s.Close()
		t.Fatal("高版本数据库被旧程序接受")
	}
}

package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlySchemaInspection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if _, e := InspectVersion(dir); e == nil {
		t.Fatal("不存在的数据库被接受")
	}
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		t.Fatal("只读检查创建了数据目录")
	}
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if version, e := InspectVersion(dir); e != nil || version != CurrentVersion {
		t.Fatal(version, e)
	}
}

func TestRefuseNewerDatabase(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec("INSERT INTO schema_migrations VALUES(?)", CurrentVersion+1); e != nil {
		t.Fatal(e)
	}
	s.Close()
	if s, e = Open(dir); e == nil {
		s.Close()
		t.Fatal("高版本数据库被旧程序接受")
	}
}

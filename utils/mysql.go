package utils

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/axllent/ssbak/app"
)

// MySQLDumpToGz uses mysqldump to stream a database dump directly into a gzip file
func MySQLDumpToGz(gzipFile string) error {
	mysqldump, err := which("mysqldump")
	if err != nil {
		return err
	}

	args := []string{"--skip-opt",
		"--add-drop-table",
		"--extended-insert",
		"--create-options",
		"--quick",
		"--set-charset",
		"--default-character-set=utf8",
		"--compress",
		"--no-tablespaces",
	}

	if dbHasColumnStatistics() {
		args = append(args, "--column-statistics=0")
	}

	if app.DB.Port != "" {
		args = append(args, "-P", app.DB.Port)
	}

	args = append(args, "-h", app.DB.Host, "-u", app.DB.Username)

	// older versions of mysqldump do not support exported passwords
	if app.DB.Password != "" {
		args = append(args, "-p"+app.DB.Password)
	}

	args = append(args, app.DB.Name)

	cmd := exec.Command(mysqldump, args...) // #nosec

	// eventually this should be supported, but not yet (eg: mysql on ubuntu 18)
	// if app.DB.Password != "" {
	// 	// Export MySQL password
	// 	cmd.Env = append(os.Environ(), "MYSQL_PWD="+app.DB.Password)
	// }

	app.Log(fmt.Sprintf("Dumping database to '%s'", gzipFile))

	f, err := os.Create(gzipFile)
	if err != nil {
		return fmt.Errorf("Error creating database backup: %s", err.Error())
	}

	defer func() {
		if err := f.Close(); err != nil {
			fmt.Printf("Error closing file: %s\n", err)
		}
	}()

	gzw := gzip.NewWriter(f)
	defer gzw.Close()
	defer gzw.Flush()

	var errbuf bytes.Buffer
	cmd.Stderr = &errbuf

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("Error dumping database: %s", err.Error())
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Error dumping database: %s", err.Error())
	}

	/* #nosec  - file is streamed from pipe to gzip file */
	if _, err := io.Copy(gzw, pipe); err != nil {
		return fmt.Errorf("Error compressing database: %s", err.Error())
	}

	if errbuf.String() != "" {
		errorStr := strings.TrimSpace(errbuf.String())
		// if MySQL returns a warning about password on the commandline, ignore, else return error
		if !strings.HasSuffix(errorStr, "Using a password on the command line interface can be insecure.") &&
			!strings.HasSuffix(errorStr, "pass --set-gtid-purged=OFF. To make a complete dump, pass --all-databases --triggers --routines --events.") {
			return errors.New(errorStr)
		}
	}

	outSize, _ := CalcSize(gzipFile)
	app.Log(fmt.Sprintf("Wrote %s (%s)", gzipFile, ByteToHr(outSize)))

	return nil
}

// MySQLCreateDB a database, optionally dropping it
func MySQLCreateDB(dropDatabase bool) error {
	mysql, err := which("mysql")
	if err != nil {
		return err
	}

	args := []string{
		"--default-character-set=utf8",
		"--compress",
	}

	if app.DB.Port != "" {
		args = append(args, "-P", app.DB.Port)
	}

	sql := "CREATE DATABASE IF NOT EXISTS `" + app.DB.Name + "`"
	if dropDatabase {
		app.Log(fmt.Sprintf("Dropping database `%s`", app.DB.Name))
		sql = "DROP DATABASE IF EXISTS `" + app.DB.Name + "`; " + sql
	}

	app.Log(fmt.Sprintf("Creating database (if not exists) `%s`", app.DB.Name))

	args = append(args, "-h", app.DB.Host, "-u", app.DB.Username)

	// older versions of mysqldump do not support exported passwords
	if app.DB.Password != "" {
		args = append(args, "-p"+app.DB.Password)
	}

	args = append(args, "-e", sql)

	cmd := exec.Command(mysql, args...) // #nosec

	// eventually this should be supported, but not yet (eg: mysql on ubuntu 18)
	// if app.DB.Password != "" {
	// 	// Export MySQL password
	// 	cmd.Env = append(os.Environ(), "MYSQL_PWD="+app.DB.Password)
	// }

	var errbuf bytes.Buffer
	cmd.Stderr = &errbuf
	if err := cmd.Run(); err != nil {
		return err
	}

	if errbuf.String() != "" {
		errorStr := strings.TrimSpace(errbuf.String())
		// if MySQL returns a warning about password on the commandline, ignore, else return error
		if !strings.HasSuffix(errorStr, "Using a password on the command line interface can be insecure.") {
			return errors.New(errorStr)
		}
	}

	return nil
}

// MySQLLoadFromGz loads a GZ database file into the database,
// streaming the gz file to the mysql cli.
func MySQLLoadFromGz(gzipSQLFile string) error {
	mysql, err := which("mysql")
	if err != nil {
		return err
	}

	if !IsFile(gzipSQLFile) {
		return fmt.Errorf("File '%s' does not exist", gzipSQLFile)
	}

	args := []string{"--default-character-set=utf8"}

	args = append(args, "-h", app.DB.Host, "-u", app.DB.Username)

	// older versions of mysqldump do not support exported passwords
	if app.DB.Password != "" {
		args = append(args, "-p"+app.DB.Password)
	}

	args = append(args, app.DB.Name)

	cmd := exec.Command(mysql, args...) // #nosec

	// eventually this should be supported, but not yet (eg: mysql on ubuntu 18)
	// if app.DB.Password != "" {
	// 	// Export MySQL password
	// 	cmd.Env = append(os.Environ(), "MYSQL_PWD="+app.DB.Password)
	// }

	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Fatal(err)
	}

	f, err := os.Open(filepath.Clean(gzipSQLFile))
	if err != nil {
		return err
	}

	defer func() {
		if err := f.Close(); err != nil {
			fmt.Printf("Error closing file: %s\n", err)
		}
	}()

	reader, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer reader.Close()

	go func() {
		defer stdin.Close()
		/* #nosec  - file is streamed from pipe to gzip file */
		if _, err := io.Copy(stdin, reader); err != nil {
			panic(err)
		}
	}()

	if _, err := cmd.CombinedOutput(); err != nil {
		return err
	}

	app.Log(fmt.Sprintf("Imported '%s' to `%s`", gzipSQLFile, app.DB.Name))

	return nil
}

// Run MySQL 8 compatibility check to see if --column-statistics=0 must get added.
// If command returns an error then it doesn't support it.
// @see: https://github.com/silverstripe/sspak/issues/81
func dbHasColumnStatistics() bool {
	mysqldump, err := which("mysqldump")
	if err != nil {
		return false
	}

	args := []string{"--no-data", "--column-statistics=0"}
	if app.DB.Port != "" {
		args = append(args, "-P", app.DB.Port)
	}
	args = append(args, "-h", app.DB.Host, "-u", app.DB.Username)

	// older versions of mysqldump do not support exported passwords
	if app.DB.Password != "" {
		args = append(args, "-p"+app.DB.Password)
	}

	args = append(args, app.DB.Name)

	cmd := exec.Command(mysqldump, args...) // #nosec

	// eventually this should be supported, but not yet (eg: mysql on ubuntu 18)
	// if app.DB.Password != "" {
	// 	// Export MySQL password
	// 	cmd.Env = append(os.Environ(), "MYSQL_PWD="+app.DB.Password)
	// }

	err = cmd.Run()

	return err == nil
}

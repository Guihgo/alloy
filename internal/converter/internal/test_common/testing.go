package test_common

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/grafana/alloy/internal/alloy"
	"github.com/grafana/alloy/internal/alloy/logging"
	"github.com/grafana/alloy/internal/converter/diag"
	"github.com/grafana/alloy/internal/featuregate"
	"github.com/grafana/alloy/internal/service"
	cluster_service "github.com/grafana/alloy/internal/service/cluster"
	http_service "github.com/grafana/alloy/internal/service/http"
	"github.com/grafana/alloy/internal/service/labelstore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

const (
	alloySuffix = ".alloy"
	diagsSuffix = ".diags"
)

// TestDirectory will execute tests for converting from a source configuration
// file to an Alloy configuration file for all files in a provided folder path.
//
// For each file in the folderPath which ends with the sourceSuffix:
//
//  1. Execute the convert func on the content of each file.
//  2. Remove an Info diags from the results of calling convert in step 1.
//  3. If the current filename.sourceSuffix has a matching filename.diags, read
//     the contents of filename.diags and validate that they match in order
//     with the diags from step 2.
//  4. If the current filename.sourceSuffix has a matching filename.alloy, read
//     the contents of filename.alloy and validate that they match the Alloy
//     configuration generated by calling convert in step 1.
func TestDirectory(t *testing.T, folderPath string, sourceSuffix string, loadAlloyConfig bool, extraArgs []string, convert func(in []byte, extraArgs []string) ([]byte, diag.Diagnostics)) {
	require.NoError(t, filepath.WalkDir(folderPath, func(path string, d fs.DirEntry, _ error) error {
		// Only skip iterating child folders
		if d.IsDir() && path != folderPath {
			return filepath.SkipDir
		}

		if strings.HasSuffix(path, sourceSuffix) {
			tc := filepath.Base(path)
			t.Run(tc, func(t *testing.T) {
				alloyFile := strings.TrimSuffix(path, sourceSuffix) + alloySuffix
				diagsFile := strings.TrimSuffix(path, sourceSuffix) + diagsSuffix
				if !fileExists(alloyFile) && !fileExists(diagsFile) {
					t.Fatalf("no expected diags or Alloy for %s - missing test expectations?", path)
				}

				actualAlloy, actualDiags := convert(getSourceContents(t, path), extraArgs)

				// Skip Info level diags for this testing. These would create
				// a lot of unnecessary noise.
				actualDiags.RemoveDiagsBySeverity(diag.SeverityLevelInfo)

				expectedDiags := getExpectedDiags(t, diagsFile)
				validateDiags(t, expectedDiags, actualDiags)

				expectedAlloy := getExpectedAlloy(t, alloyFile)
				validateAlloy(t, expectedAlloy, actualAlloy, loadAlloyConfig)
			})
		}

		return nil
	}))
}

// getSourceContents reads the source file and retrieve its contents.
func getSourceContents(t *testing.T, path string) []byte {
	sourceBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	return sourceBytes
}

// getExpectedDiags will retrieve any expected diags for the test.
func getExpectedDiags(t *testing.T, diagsFile string) []string {
	expectedDiags := []string{}
	if _, err := os.Stat(diagsFile); err == nil {
		errorBytes, err := os.ReadFile(diagsFile)
		require.NoError(t, err)

		br := bufio.NewScanner(bytes.NewReader(errorBytes))
		for br.Scan() {
			// Some error messages have newlines in them; replace \n in strings with
			// literal newlines to allow them to match.
			sanitizedLine := strings.ReplaceAll(br.Text(), "\\n", "\n")
			if sanitizedLine == "" {
				// Ignore empty lines.
				continue
			}
			expectedDiags = append(expectedDiags, sanitizedLine)
		}
	}

	return expectedDiags
}

// validateDiags makes sure the expected diags and actual diags are a match
func validateDiags(t *testing.T, expectedDiags []string, actualDiags diag.Diagnostics) {
	for ix, diag := range actualDiags {
		if len(expectedDiags) > ix {
			if expectedDiags[ix] != diag.String() {
				printActualDiags(actualDiags)
			}
			require.Equal(t, expectedDiags[ix], diag.String())
		} else {
			printActualDiags(actualDiags)
			require.Fail(t, "unexpected diag count reach for diag: "+diag.String())
		}
	}

	// If we expect more diags than we got
	if len(expectedDiags) > len(actualDiags) {
		printActualDiags(actualDiags)
		require.Fail(t, "missing expected diag: "+expectedDiags[len(actualDiags)])
	}
}

func printActualDiags(actualDiags diag.Diagnostics) {
	fmt.Println("============== ACTUAL =============")
	fmt.Println(string(normalizeLineEndings([]byte(actualDiags.Error()))))
	fmt.Println("===================================")
}

// normalizeLineEndings will replace '\r\n' with '\n'.
func normalizeLineEndings(data []byte) []byte {
	normalized := bytes.ReplaceAll(data, []byte{'\r', '\n'}, []byte{'\n'})
	return normalized
}

// getExpectedAlloy reads the expected Alloy output file and retrieve its contents.
func getExpectedAlloy(t *testing.T, filePath string) []byte {
	if _, err := os.Stat(filePath); err == nil {
		outputBytes, err := os.ReadFile(filePath)
		require.NoError(t, err)
		return normalizeLineEndings(outputBytes)
	}

	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// validateAlloy makes sure the expected Alloy and actual Alloy are a match
func validateAlloy(t *testing.T, expectedAlloy []byte, actualAlloy []byte, loadAlloyConfig bool) {
	if len(expectedAlloy) > 0 {
		if !reflect.DeepEqual(expectedAlloy, actualAlloy) {
			fmt.Println("============== ACTUAL =============")
			fmt.Println(string(normalizeLineEndings(actualAlloy)))
			fmt.Println("===================================")
		}

		require.Equal(t, string(expectedAlloy), string(normalizeLineEndings(actualAlloy)))

		if loadAlloyConfig {
			attemptLoadingAlloyConfig(t, actualAlloy)
		}
	}
}

// attemptLoadingAlloyConfig will attempt to load the Alloy config and report any errors.
func attemptLoadingAlloyConfig(t *testing.T, bb []byte) {
	cfg, err := alloy.ParseSource(t.Name(), bb)
	require.NoError(t, err, "the output Alloy config failed to parse: %s", string(normalizeLineEndings(bb)))

	// The below check suffers from test race conditions on Windows. Our goal here is to verify config conversions,
	// which is platform independent, so we can skip this check on Windows as a workaround.
	if runtime.GOOS == "windows" {
		return
	}

	logger, err := logging.New(os.Stderr, logging.DefaultOptions)
	require.NoError(t, err)

	clusterService, err := cluster_service.New(cluster_service.Options{
		Log:              logger,
		EnableClustering: false,
		NodeName:         "test-node",
		AdvertiseAddress: "127.0.0.1:80",
	})
	require.NoError(t, err)

	f := alloy.New(alloy.Options{
		Logger:       logger,
		DataPath:     t.TempDir(),
		MinStability: featuregate.StabilityExperimental,
		Services: []service.Service{
			// The services here aren't used, but we still need to provide an
			// implementations so that components which rely on the services load
			// properly.
			http_service.New(http_service.Options{}),
			clusterService,
			labelstore.New(nil, prometheus.DefaultRegisterer),
		},
	})
	err = f.LoadSource(cfg, nil)

	// Many components will fail to build as e.g. the cert files are missing, so we ignore these errors.
	// This is not ideal, but we still validate for other potential issues.
	if err != nil && strings.Contains(err.Error(), "Failed to build component") {
		t.Log("ignoring error: " + err.Error())
		return
	}
	require.NoError(t, err, "failed to load the Alloy config: %s", string(normalizeLineEndings(bb)))
}
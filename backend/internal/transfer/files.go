package transfer

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxNDJSONLineBytes = 4 << 20

func writeExport(output string, manifest Manifest, content dataset) (Manifest, error) {
	output, err := cleanAbsolutePath(output, "export output")
	if err != nil {
		return Manifest{}, err
	}
	if _, err := os.Lstat(output); err == nil {
		return Manifest{}, fmt.Errorf("export output already exists: %s", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("inspect export output: %w", err)
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return Manifest{}, fmt.Errorf("create export parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".canvas-export-")
	if err != nil {
		return Manifest{}, fmt.Errorf("create temporary export: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := os.Chmod(temporary, 0700); err != nil {
		return Manifest{}, fmt.Errorf("set export directory permissions: %w", err)
	}

	manifest.Files = make(map[string]FileSummary, len(dataFiles))
	writers := []struct {
		name  string
		value any
	}{
		{projectsFile, content.Projects},
		{assetsFile, content.Assets},
		{projectRefsFile, content.ProjectAssetRefs},
		{libraryItemsFile, content.LibraryItems},
		{editorDocumentsFile, content.EditorDocuments},
		{editorRefsFile, content.EditorAssetRefs},
		{editorRevisionsFile, content.EditorRevisions},
		{modelPoliciesFile, content.ModelPolicies},
		{modelPolicyItemsFile, content.ModelPolicyItems},
		{policyAuditsFile, content.PolicyAudits},
		{runtimeSettingsFile, content.RuntimeSettings},
		{jobsFile, content.Jobs},
		{jobInputsFile, content.JobInputs},
		{jobResultsFile, content.JobResults},
		{legacyMediaTasksFile, content.LegacyMediaTasks},
		{objectsFile, content.Objects},
	}
	for _, item := range writers {
		summary, err := writeNDJSON(filepath.Join(temporary, item.name), item.value)
		if err != nil {
			return Manifest{}, fmt.Errorf("write %s: %w", item.name, err)
		}
		manifest.Files[item.name] = summary
	}
	checksumSummary, err := writeObjectChecksums(filepath.Join(temporary, objectsChecksumFile), content.Objects)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Files[objectsChecksumFile] = checksumSummary
	manifest.Counts = datasetCounts(content)
	manifest.Objects = objectSummary(content.Objects, manifest.Files[objectsFile].SHA256)
	manifest.ContentSHA256 = contentDigest(manifest.Files)
	if err := validateDataset(manifest, content); err != nil {
		return Manifest{}, fmt.Errorf("validate export: %w", err)
	}
	if err := writeJSONFile(filepath.Join(temporary, manifestFile), manifest); err != nil {
		return Manifest{}, fmt.Errorf("write manifest: %w", err)
	}
	if err := syncDirectory(temporary); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(temporary, output); err != nil {
		return Manifest{}, fmt.Errorf("publish export: %w", err)
	}
	committed = true
	if err := syncDirectory(parent); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func readExport(input string) (Manifest, dataset, error) {
	input, err := validatePrivateDirectory(input, "export input")
	if err != nil {
		return Manifest{}, dataset{}, err
	}
	var manifest Manifest
	if err := readJSONFile(filepath.Join(input, manifestFile), &manifest); err != nil {
		return Manifest{}, dataset{}, fmt.Errorf("read manifest: %w", err)
	}
	if manifest.Format != Format {
		return Manifest{}, dataset{}, fmt.Errorf("unsupported export format %q", manifest.Format)
	}
	if len(manifest.Files) != len(dataFiles) {
		return Manifest{}, dataset{}, errors.New("manifest file set is incomplete or contains unknown files")
	}
	for _, name := range dataFiles {
		expected, ok := manifest.Files[name]
		if !ok {
			return Manifest{}, dataset{}, fmt.Errorf("manifest is missing %s", name)
		}
		actual, err := summarizeFile(filepath.Join(input, name), name != objectsChecksumFile)
		if err != nil {
			return Manifest{}, dataset{}, err
		}
		if actual != expected {
			return Manifest{}, dataset{}, fmt.Errorf("export file checksum or size mismatch: %s", name)
		}
	}
	if got := contentDigest(manifest.Files); got != manifest.ContentSHA256 {
		return Manifest{}, dataset{}, errors.New("export content digest mismatch")
	}

	var content dataset
	readers := []struct {
		name   string
		target any
	}{
		{projectsFile, &content.Projects},
		{assetsFile, &content.Assets},
		{projectRefsFile, &content.ProjectAssetRefs},
		{libraryItemsFile, &content.LibraryItems},
		{editorDocumentsFile, &content.EditorDocuments},
		{editorRefsFile, &content.EditorAssetRefs},
		{editorRevisionsFile, &content.EditorRevisions},
		{modelPoliciesFile, &content.ModelPolicies},
		{modelPolicyItemsFile, &content.ModelPolicyItems},
		{policyAuditsFile, &content.PolicyAudits},
		{runtimeSettingsFile, &content.RuntimeSettings},
		{jobsFile, &content.Jobs},
		{jobInputsFile, &content.JobInputs},
		{jobResultsFile, &content.JobResults},
		{legacyMediaTasksFile, &content.LegacyMediaTasks},
		{objectsFile, &content.Objects},
	}
	for _, item := range readers {
		if err := readNDJSON(filepath.Join(input, item.name), item.target); err != nil {
			return Manifest{}, dataset{}, fmt.Errorf("decode %s: %w", item.name, err)
		}
	}
	if err := validateDataset(manifest, content); err != nil {
		return Manifest{}, dataset{}, err
	}
	return manifest, content, nil
}

func writeNDJSON(path string, value any) (FileSummary, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return FileSummary{}, err
	}
	hasher := sha256.New()
	counter := &countingWriter{writer: io.MultiWriter(file, hasher)}
	encoder := json.NewEncoder(counter)
	encoder.SetEscapeHTML(false)
	rows, err := encodeSlice(encoder, value)
	if err != nil {
		_ = file.Close()
		return FileSummary{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return FileSummary{}, err
	}
	if err := file.Close(); err != nil {
		return FileSummary{}, err
	}
	return FileSummary{Rows: rows, Bytes: counter.bytes, SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

func encodeSlice(encoder *json.Encoder, value any) (int, error) {
	rows := 0
	switch items := value.(type) {
	case []Project:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []Asset:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []ProjectAssetRef:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []LibraryItem:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []EditorDocument:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []EditorAssetRef:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []EditorRevision:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []ModelPolicy:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []ModelPolicyItem:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []PolicyAudit:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []RuntimeSetting:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []Job:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []JobInput:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []JobResult:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []LegacyMediaTask:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	case []Object:
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return rows, err
			}
			rows++
		}
	default:
		return 0, fmt.Errorf("unsupported NDJSON slice type %T", value)
	}
	return rows, nil
}

func readNDJSON(path string, target any) error {
	file, err := openRegularFile(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxNDJSONLineBytes)
	line := 0
	for scanner.Scan() {
		line++
		payload := append([]byte(nil), scanner.Bytes()...)
		if len(payload) == 0 {
			return fmt.Errorf("empty line %d", line)
		}
		if err := appendDecoded(payload, target); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
	}
	return scanner.Err()
}

func appendDecoded(payload []byte, target any) error {
	decode := func(value any) error {
		decoder := json.NewDecoder(strings.NewReader(string(payload)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(value); err != nil {
			return err
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("multiple JSON values")
		}
		return nil
	}
	switch items := target.(type) {
	case *[]Project:
		var value Project
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]Asset:
		var value Asset
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]ProjectAssetRef:
		var value ProjectAssetRef
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]LibraryItem:
		var value LibraryItem
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]EditorDocument:
		var value EditorDocument
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]EditorAssetRef:
		var value EditorAssetRef
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]EditorRevision:
		var value EditorRevision
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]ModelPolicy:
		var value ModelPolicy
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]ModelPolicyItem:
		var value ModelPolicyItem
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]PolicyAudit:
		var value PolicyAudit
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]RuntimeSetting:
		var value RuntimeSetting
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]Job:
		var value Job
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]JobInput:
		var value JobInput
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]JobResult:
		var value JobResult
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]LegacyMediaTask:
		var value LegacyMediaTask
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	case *[]Object:
		var value Object
		if err := decode(&value); err != nil {
			return err
		}
		*items = append(*items, value)
	default:
		return fmt.Errorf("unsupported NDJSON target type %T", target)
	}
	return nil
}

func writeObjectChecksums(path string, objects []Object) (FileSummary, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return FileSummary{}, fmt.Errorf("create object checksum file: %w", err)
	}
	hasher := sha256.New()
	counter := &countingWriter{writer: io.MultiWriter(file, hasher)}
	for _, object := range objects {
		if _, err := fmt.Fprintf(counter, "%s  %s\n", object.SHA256, object.TargetKey); err != nil {
			_ = file.Close()
			return FileSummary{}, fmt.Errorf("write object checksum: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return FileSummary{}, err
	}
	if err := file.Close(); err != nil {
		return FileSummary{}, err
	}
	return FileSummary{Rows: len(objects), Bytes: counter.bytes, SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

func writeJSONFile(path string, value any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func readJSONFile(path string, target any) error {
	file, err := openRegularFile(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func summarizeFile(path string, countRows bool) (FileSummary, error) {
	file, err := openRegularFile(path)
	if err != nil {
		return FileSummary{}, err
	}
	defer file.Close()
	hasher := sha256.New()
	rows := 0
	last := byte(0)
	buffer := make([]byte, 64<<10)
	var size int64
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			chunk := buffer[:read]
			_, _ = hasher.Write(chunk)
			size += int64(read)
			if countRows {
				rows += strings.Count(string(chunk), "\n")
			}
			last = chunk[len(chunk)-1]
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return FileSummary{}, readErr
		}
	}
	if countRows && size > 0 && last != '\n' {
		return FileSummary{}, errors.New("NDJSON file lacks trailing newline")
	}
	if !countRows {
		rows = strings.Count(readFileForLineCount(path), "\n")
	}
	return FileSummary{Rows: rows, Bytes: size, SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

func readFileForLineCount(path string) string {
	payload, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(payload)
}

func contentDigest(files map[string]FileSummary) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	hasher := sha256.New()
	for _, name := range names {
		summary := files[name]
		fmt.Fprintf(hasher, "%s\x00%d\x00%d\x00%s\x00", name, summary.Rows, summary.Bytes, summary.SHA256)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func objectSummary(objects []Object, digest string) ObjectSummary {
	result := ObjectSummary{Count: len(objects), SHA256: digest}
	for _, object := range objects {
		result.Bytes += object.Size
	}
	return result
}

func datasetCounts(content dataset) map[string]int {
	result := map[string]int{
		"projects": len(content.Projects), "assets": len(content.Assets),
		"project_asset_refs": len(content.ProjectAssetRefs), "library_items": len(content.LibraryItems),
		"editor_documents": len(content.EditorDocuments), "editor_asset_refs": len(content.EditorAssetRefs),
		"editor_revisions": len(content.EditorRevisions), "model_policies": len(content.ModelPolicies),
		"model_policy_items": len(content.ModelPolicyItems), "model_policy_audits": len(content.PolicyAudits),
		"runtime_settings": len(content.RuntimeSettings), "jobs": len(content.Jobs),
		"job_inputs": len(content.JobInputs), "job_results": len(content.JobResults),
		"legacy_media_tasks": len(content.LegacyMediaTasks), "objects": len(content.Objects),
	}
	for _, item := range content.Projects {
		if item.DeletedAt == nil {
			result["projects_active"]++
		} else {
			result["projects_deleted"]++
		}
	}
	for _, item := range content.Assets {
		if item.DeletedAt == nil {
			result["assets_active"]++
		} else {
			result["assets_deleted"]++
		}
	}
	for _, item := range content.LibraryItems {
		if item.DeletedAt == nil {
			result["library_items_active"]++
		} else {
			result["library_items_deleted"]++
		}
	}
	for _, item := range content.EditorDocuments {
		if item.DeletedAt == nil {
			result["editor_documents_active"]++
		} else {
			result["editor_documents_deleted"]++
		}
	}
	return result
}

func cleanAbsolutePath(value, label string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path", label)
	}
	cleaned := filepath.Clean(value)
	if cleaned == string(filepath.Separator) {
		return "", fmt.Errorf("%s must not be the filesystem root", label)
	}
	return cleaned, nil
}

func validatePrivateDirectory(value, label string) (string, error) {
	cleaned, err := cleanAbsolutePath(value, label)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s must be a real directory", label)
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("%s permissions must not grant group or other access", label)
	}
	return cleaned, nil
}

func openRegularFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	return os.Open(path)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

type countingWriter struct {
	writer io.Writer
	bytes  int64
}

func (writer *countingWriter) Write(payload []byte) (int, error) {
	written, err := writer.writer.Write(payload)
	writer.bytes += int64(written)
	return written, err
}

package study

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// TaskCatalogSchemaVersion identifies the sole supported task-catalog wire
	// contract.
	TaskCatalogSchemaVersion = "tokenbench.task-catalog/v1"

	taskCatalogTaskCount       = 144
	taskCatalogRepositoryCount = 12
	tasksPerCatalogRepository  = 12
	tasksPerCatalogLanguage    = 24
	tasksPerCatalogFamily      = 48
	tasksPerCatalogTier        = 36
	maxTaskCatalogBytes        = 16 << 20
	maxCatalogCommands         = 16
	maxCatalogCommandArguments = 64
	maxCatalogCommandArgument  = 4_096
	maxCatalogCommandBytes     = 64 << 10
	maxCatalogCommandTimeoutMS = int64(2 * 60 * 60 * 1_000)
	maxCatalogExclusions       = 32
	maxCatalogComponents       = 30
)

// CatalogLanguage is one of the six languages in the locked v1 corpus.
type CatalogLanguage string

const (
	CatalogLanguageCPP        CatalogLanguage = "cpp"
	CatalogLanguageGo         CatalogLanguage = "go"
	CatalogLanguageRust       CatalogLanguage = "rust"
	CatalogLanguageJava       CatalogLanguage = "java"
	CatalogLanguagePython     CatalogLanguage = "python"
	CatalogLanguageTypeScript CatalogLanguage = "typescript"
)

// CatalogTaskFamily identifies the kind of benchmark work.
type CatalogTaskFamily string

const (
	CatalogFamilyCode    CatalogTaskFamily = "code"
	CatalogFamilyReview  CatalogTaskFamily = "review"
	CatalogFamilyExplain CatalogTaskFamily = "explain"
)

// CatalogTaskTier identifies the bounded investigation size.
type CatalogTaskTier string

const (
	CatalogTierSmall  CatalogTaskTier = "small"
	CatalogTierMedium CatalogTaskTier = "medium"
	CatalogTierLarge  CatalogTaskTier = "large"
	CatalogTierHuge   CatalogTaskTier = "huge"
)

var (
	catalogLanguages = []CatalogLanguage{
		CatalogLanguageCPP,
		CatalogLanguageGo,
		CatalogLanguageJava,
		CatalogLanguagePython,
		CatalogLanguageRust,
		CatalogLanguageTypeScript,
	}
	catalogFamilies = []CatalogTaskFamily{
		CatalogFamilyCode,
		CatalogFamilyExplain,
		CatalogFamilyReview,
	}
	catalogTiers = []CatalogTaskTier{
		CatalogTierHuge,
		CatalogTierLarge,
		CatalogTierMedium,
		CatalogTierSmall,
	}
	lockedCatalogRepositories = []catalogRepositoryIdentity{
		{CatalogLanguageCPP, "fmt", "corpus-cpp-fmt", "https://github.com/fmtlib/fmt"},
		{CatalogLanguageCPP, "seastar", "corpus-cpp-seastar", "https://github.com/scylladb/seastar"},
		{CatalogLanguageGo, "chi", "corpus-go-chi", "https://github.com/go-chi/chi"},
		{CatalogLanguageGo, "go-git", "corpus-go-go-git", "https://github.com/go-git/go-git"},
		{CatalogLanguageJava, "commons-lang", "corpus-java-commons-lang", "https://github.com/apache/commons-lang"},
		{CatalogLanguageJava, "scylla-driver", "corpus-java-scylla-driver", "https://github.com/scylladb/java-driver"},
		{CatalogLanguagePython, "click", "corpus-python-click", "https://github.com/pallets/click"},
		{CatalogLanguagePython, "scylla-ccm", "corpus-python-scylla-ccm", "https://github.com/scylladb/scylla-ccm"},
		{CatalogLanguageRust, "clap", "corpus-rust-clap", "https://github.com/clap-rs/clap"},
		{CatalogLanguageRust, "scylla-driver", "corpus-rust-scylla-driver", "https://github.com/scylladb/scylla-rust-driver"},
		{CatalogLanguageTypeScript, "got", "corpus-typescript-got", "https://github.com/sindresorhus/got"},
		{CatalogLanguageTypeScript, "kysely", "corpus-typescript-kysely", "https://github.com/kysely-org/kysely"},
	}
)

type catalogRepositoryIdentity struct {
	language       CatalogLanguage
	taskSlug       string
	repositorySlug string
	upstreamURL    string
}

// TaskCatalog is the closed v1 authoring catalog. Tasks are strictly sorted by
// task_id so the same catalog has one byte representation.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical wire order.
type TaskCatalog struct {
	SchemaVersion string        `json:"schema_version"`
	CatalogID     string        `json:"catalog_id"`
	Tasks         []CatalogTask `json:"tasks"`
}

// CatalogTask binds one prompt and evaluator to an immutable source state.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical wire order.
type CatalogTask struct {
	TaskID                      string             `json:"task_id"`
	Language                    CatalogLanguage    `json:"language"`
	RepoSlug                    string             `json:"repo_slug"`
	RepositorySlug              string             `json:"repository_slug"`
	Source                      CatalogSource      `json:"source"`
	Family                      CatalogTaskFamily  `json:"family"`
	Tier                        CatalogTaskTier    `json:"tier"`
	PromptSHA256                string             `json:"prompt_sha256"`
	ToolchainSHA256             string             `json:"toolchain_sha256"`
	Commands                    []CatalogCommand   `json:"commands"`
	Ceilings                    CatalogCeilings    `json:"ceilings"`
	Facts                       []FactItem         `json:"facts"`
	Rubric                      []RubricItem       `json:"rubric"`
	HiddenEvaluatorBundleSHA256 string             `json:"hidden_evaluator_bundle_sha256"`
	GoldPatch                   *CatalogGoldPatch  `json:"gold_patch"`
	Exclusions                  []CatalogExclusion `json:"exclusions"`
}

// CatalogSource records the immutable upstream and corpus-copy provenance used
// to construct one task. HeadObjectID is explicitly null when no comparison
// revision applies.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical wire order.
type CatalogSource struct {
	UpstreamURL      string  `json:"upstream_url"`
	SourceURL        string  `json:"source_url"`
	BaseObjectID     string  `json:"base_object_id"`
	HeadObjectID     *string `json:"head_object_id"`
	SourceTreeSHA256 string  `json:"source_tree_sha256"`
}

// CatalogCommand is a direct argv and its individual execution timeout. This
// package records commands but never executes them.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical wire order.
type CatalogCommand struct {
	CommandID     string   `json:"command_id"`
	Argv          []string `json:"argv"`
	TimeoutMillis int64    `json:"timeout_millis"`
}

// CatalogCeilings fixes the tier's navigation, patch, and component bounds.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical wire order.
type CatalogCeilings struct {
	NavigationFileMinimum int `json:"navigation_file_minimum"`
	NavigationFileCeiling int `json:"navigation_file_ceiling"`
	ChangeLineCeiling     int `json:"change_line_ceiling"`
	ComponentMinimum      int `json:"component_minimum"`
	ComponentCeiling      int `json:"component_ceiling"`
}

// CatalogGoldPatch identifies the hidden expected patch and its resulting Git
// tree. It is required only for code tasks.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical wire order.
type CatalogGoldPatch struct {
	PatchSHA256        string `json:"patch_sha256"`
	ResultTreeObjectID string `json:"result_tree_object_id"`
}

// CatalogExclusion preregisters one objective condition under which a task is
// omitted from the confirmatory analysis.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical wire order.
type CatalogExclusion struct {
	Code      string `json:"code"`
	Condition string `json:"condition"`
}

// DecodeTaskCatalog accepts only the byte-exact canonical required-field JSON
// form and rejects inputs above the v1 size limit before decoding.
func DecodeTaskCatalog(raw []byte) (TaskCatalog, error) {
	if len(raw) > maxTaskCatalogBytes {
		return TaskCatalog{}, fmt.Errorf("task catalog exceeds %d bytes", maxTaskCatalogBytes)
	}
	var catalog TaskCatalog
	if err := decodeCanonical(raw, &catalog); err != nil {
		return TaskCatalog{}, fmt.Errorf("decode task catalog: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return TaskCatalog{}, err
	}
	return catalog, nil
}

// EncodeTaskCatalog returns the sole canonical JSON representation of a valid
// v1 catalog. It never sorts or repairs caller input.
func EncodeTaskCatalog(catalog TaskCatalog) ([]byte, error) {
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	return canonicalJSON(catalog)
}

// SHA256 returns the identity of the canonical catalog.
func (catalog TaskCatalog) SHA256() (string, error) {
	if err := catalog.Validate(); err != nil {
		return "", err
	}
	return canonicalDigest(catalog)
}

// Validate checks the complete closed corpus, distribution, provenance, task
// identity, safety bounds, and evaluator identities.
func (catalog TaskCatalog) Validate() error {
	if catalog.SchemaVersion != TaskCatalogSchemaVersion {
		return fmt.Errorf("unexpected task catalog schema %q", catalog.SchemaVersion)
	}
	if !validID(catalog.CatalogID) {
		return errors.New("catalog_id is not a canonical identifier")
	}
	if len(catalog.Tasks) != taskCatalogTaskCount {
		return fmt.Errorf("task catalog must contain exactly %d tasks", taskCatalogTaskCount)
	}

	repositoryCounts := make(map[string]int, taskCatalogRepositoryCount)
	languageCounts := make(map[CatalogLanguage]int, len(catalogLanguages))
	familyCounts := make(map[CatalogTaskFamily]int, len(catalogFamilies))
	tierCounts := make(map[CatalogTaskTier]int, len(catalogTiers))
	for index, task := range catalog.Tasks {
		if index > 0 && catalog.Tasks[index-1].TaskID >= task.TaskID {
			return errors.New("catalog tasks are not strictly sorted by task_id")
		}
		identity, ok := findCatalogRepository(task.Language, task.RepoSlug)
		if !ok {
			return fmt.Errorf("task %q: language/repo_slug is not in the locked v1 corpus", task.TaskID)
		}
		if err := validateCatalogTask(task, identity); err != nil {
			return fmt.Errorf("task %q: %w", task.TaskID, err)
		}
		repositoryCounts[repositoryCountKey(task.Language, task.RepoSlug)]++
		languageCounts[task.Language]++
		familyCounts[task.Family]++
		tierCounts[task.Tier]++
	}

	for _, identity := range lockedCatalogRepositories {
		if count := repositoryCounts[repositoryCountKey(identity.language, identity.taskSlug)]; count != tasksPerCatalogRepository {
			return fmt.Errorf("repository %s.%s has %d tasks; want %d", identity.language, identity.taskSlug, count, tasksPerCatalogRepository)
		}
	}
	for _, language := range catalogLanguages {
		if count := languageCounts[language]; count != tasksPerCatalogLanguage {
			return fmt.Errorf("language %s has %d tasks; want %d", language, count, tasksPerCatalogLanguage)
		}
	}
	for _, family := range catalogFamilies {
		if count := familyCounts[family]; count != tasksPerCatalogFamily {
			return fmt.Errorf("family %s has %d tasks; want %d", family, count, tasksPerCatalogFamily)
		}
	}
	for _, tier := range catalogTiers {
		if count := tierCounts[tier]; count != tasksPerCatalogTier {
			return fmt.Errorf("tier %s has %d tasks; want %d", tier, count, tasksPerCatalogTier)
		}
	}
	canonical, err := canonicalJSON(catalog)
	if err != nil {
		return fmt.Errorf("encode task catalog for size validation: %w", err)
	}
	if len(canonical) > maxTaskCatalogBytes {
		return fmt.Errorf("task catalog exceeds %d canonical bytes", maxTaskCatalogBytes)
	}
	return nil
}

func validateCatalogTask(task CatalogTask, identity catalogRepositoryIdentity) error {
	wantID := strings.Join([]string{string(task.Language), task.RepoSlug, string(task.Family), string(task.Tier)}, ".")
	if task.TaskID != wantID {
		return fmt.Errorf("task_id must be %q", wantID)
	}
	if task.RepositorySlug != identity.repositorySlug {
		return fmt.Errorf("repository_slug must be %q", identity.repositorySlug)
	}
	if err := validateCatalogSource(task.Source, identity); err != nil {
		return err
	}
	if !containsCatalogFamily(task.Family) {
		return fmt.Errorf("invalid family %q", task.Family)
	}
	if (task.Family == CatalogFamilyCode || task.Family == CatalogFamilyReview) &&
		task.Source.HeadObjectID == nil {
		return errors.New("code and review tasks require head_object_id")
	}
	if !containsCatalogTier(task.Tier) {
		return fmt.Errorf("invalid tier %q", task.Tier)
	}
	if !validSHA256(task.PromptSHA256) {
		return errors.New("prompt_sha256 is not a canonical SHA-256 digest")
	}
	if !validSHA256(task.ToolchainSHA256) {
		return errors.New("toolchain_sha256 is not a canonical SHA-256 digest")
	}
	if err := validateCatalogCommands(task.Commands); err != nil {
		return err
	}
	if task.Family == CatalogFamilyCode {
		if err := validateCatalogCodeCommands(task.Commands); err != nil {
			return err
		}
	}
	if err := validateCatalogCeilings(task.Tier, task.Ceilings); err != nil {
		return err
	}
	if err := validateCatalogQuality(task.Family, task.Facts, task.Rubric); err != nil {
		return err
	}
	if !validSHA256(task.HiddenEvaluatorBundleSHA256) {
		return errors.New("hidden_evaluator_bundle_sha256 is not a canonical SHA-256 digest")
	}
	if task.Family == CatalogFamilyCode {
		if task.GoldPatch == nil {
			return errors.New("code task requires gold_patch")
		}
		if !validSHA256(task.GoldPatch.PatchSHA256) ||
			!validGitObjectID(task.GoldPatch.ResultTreeObjectID) {
			return errors.New("gold_patch identity is invalid")
		}
		if len(task.GoldPatch.ResultTreeObjectID) != len(task.Source.BaseObjectID) {
			return errors.New("gold_patch result tree uses a different Git object format than base_object_id")
		}
	} else if task.GoldPatch != nil {
		return errors.New("only code tasks may declare gold_patch")
	}
	return validateCatalogExclusions(task.Exclusions)
}

func validateCatalogSource(source CatalogSource, identity catalogRepositoryIdentity) error {
	if source.UpstreamURL != identity.upstreamURL {
		return fmt.Errorf("upstream_url must be %q", identity.upstreamURL)
	}
	wantSourceURL := "https://github.com/yapless/" + identity.repositorySlug
	if source.SourceURL != wantSourceURL {
		return fmt.Errorf("source_url must be %q", wantSourceURL)
	}
	if !validGitObjectID(source.BaseObjectID) {
		return errors.New("base_object_id is not a canonical Git object ID")
	}
	if source.HeadObjectID != nil {
		if !validGitObjectID(*source.HeadObjectID) {
			return errors.New("head_object_id is not a canonical Git object ID")
		}
		if *source.HeadObjectID == source.BaseObjectID {
			return errors.New("head_object_id must differ from base_object_id")
		}
		if len(*source.HeadObjectID) != len(source.BaseObjectID) {
			return errors.New("head_object_id uses a different Git object format than base_object_id")
		}
	}
	if !validSHA256(source.SourceTreeSHA256) {
		return errors.New("source_tree_sha256 is not a canonical SHA-256 digest")
	}
	return nil
}

func validateCatalogCommands(commands []CatalogCommand) error {
	if len(commands) == 0 || len(commands) > maxCatalogCommands {
		return fmt.Errorf("commands must contain 1..%d entries", maxCatalogCommands)
	}
	for index, command := range commands {
		if index > 0 && commands[index-1].CommandID >= command.CommandID {
			return errors.New("commands are not strictly sorted by command_id")
		}
		if !validID(command.CommandID) {
			return fmt.Errorf("command %d has an invalid command_id", index)
		}
		if len(command.Argv) == 0 || len(command.Argv) > maxCatalogCommandArguments {
			return fmt.Errorf("command %q argv must contain 1..%d entries", command.CommandID, maxCatalogCommandArguments)
		}
		argumentBytes := 0
		for _, argument := range command.Argv {
			if argument == "" || len(argument) > maxCatalogCommandArgument ||
				!utf8.ValidString(argument) || strings.ContainsRune(argument, '\x00') {
				return fmt.Errorf("command %q has an invalid argv entry", command.CommandID)
			}
			argumentBytes += len(argument)
			if argumentBytes > maxCatalogCommandBytes {
				return fmt.Errorf("command %q argv exceeds %d bytes", command.CommandID, maxCatalogCommandBytes)
			}
		}
		if command.TimeoutMillis <= 0 || command.TimeoutMillis > maxCatalogCommandTimeoutMS {
			return fmt.Errorf("command %q timeout_millis must be in 1..%d", command.CommandID, maxCatalogCommandTimeoutMS)
		}
	}
	return nil
}

func validateCatalogCodeCommands(commands []CatalogCommand) error {
	required := [...]string{"build", "fail-to-pass", "pass-to-pass"}
	commandIndex := 0
	for _, requiredID := range required {
		for commandIndex < len(commands) && commands[commandIndex].CommandID < requiredID {
			commandIndex++
		}
		if commandIndex == len(commands) || commands[commandIndex].CommandID != requiredID {
			return fmt.Errorf("code task requires command_id %q", requiredID)
		}
		commandIndex++
	}
	return nil
}

func validateCatalogCeilings(tier CatalogTaskTier, ceilings CatalogCeilings) error {
	want := catalogTierCeilings(tier)
	if ceilings != want {
		return fmt.Errorf("ceilings do not match the fixed %s tier", tier)
	}
	return nil
}

func catalogTierCeilings(tier CatalogTaskTier) CatalogCeilings {
	switch tier {
	case CatalogTierSmall:
		return CatalogCeilings{
			NavigationFileMinimum: 1,
			NavigationFileCeiling: 2,
			ChangeLineCeiling:     80,
			ComponentMinimum:      1,
			ComponentCeiling:      1,
		}
	case CatalogTierMedium:
		return CatalogCeilings{
			NavigationFileMinimum: 3,
			NavigationFileCeiling: 5,
			ChangeLineCeiling:     250,
			ComponentMinimum:      1,
			ComponentCeiling:      2,
		}
	case CatalogTierLarge:
		return CatalogCeilings{
			NavigationFileMinimum: 6,
			NavigationFileCeiling: 12,
			ChangeLineCeiling:     700,
			ComponentMinimum:      2,
			ComponentCeiling:      3,
		}
	case CatalogTierHuge:
		return CatalogCeilings{
			NavigationFileMinimum: 13,
			NavigationFileCeiling: 30,
			ChangeLineCeiling:     1_500,
			ComponentMinimum:      3,
			ComponentCeiling:      maxCatalogComponents,
		}
	default:
		return CatalogCeilings{}
	}
}

func validateCatalogQuality(family CatalogTaskFamily, facts []FactItem, rubric []RubricItem) error {
	if len(facts) == 0 || len(facts) > maxQualityItems {
		return fmt.Errorf("facts must contain 1..%d entries", maxQualityItems)
	}
	if rubric == nil || len(rubric) > maxQualityItems {
		return fmt.Errorf("rubric must be a canonical array with at most %d entries", maxQualityItems)
	}
	if family != CatalogFamilyCode && len(rubric) == 0 {
		return errors.New("review and explain tasks require at least one rubric item")
	}
	if len(facts)+len(rubric) > maxQualityItems {
		return fmt.Errorf("facts and rubric exceed %d total items", maxQualityItems)
	}
	seen := make(map[string]struct{}, len(facts)+len(rubric))
	points := int64(0)
	for index, item := range facts {
		if index > 0 && facts[index-1].ItemID >= item.ItemID {
			return errors.New("fact items are not strictly sorted by item_id")
		}
		if !validID(item.ItemID) || !validEvaluationText(item.Requirement, 4_000) ||
			!validEvaluationText(item.Expected, 8_000) || item.MaximumPoints <= 0 ||
			item.MaximumPoints > maxQualityPoints {
			return fmt.Errorf("fact item %d is invalid", index)
		}
		seen[item.ItemID] = struct{}{}
		if item.MaximumPoints > maxQualityPoints-points {
			return errors.New("quality points exceed the v1 limit")
		}
		points += item.MaximumPoints
	}
	for index, item := range rubric {
		if index > 0 && rubric[index-1].ItemID >= item.ItemID {
			return errors.New("rubric items are not strictly sorted by item_id")
		}
		if !validID(item.ItemID) || !validEvaluationText(item.Requirement, 4_000) ||
			item.MaximumPoints <= 0 || item.MaximumPoints > maxQualityPoints {
			return fmt.Errorf("rubric item %d is invalid", index)
		}
		if _, duplicate := seen[item.ItemID]; duplicate {
			return fmt.Errorf("duplicate quality item_id %q", item.ItemID)
		}
		seen[item.ItemID] = struct{}{}
		if item.MaximumPoints > maxQualityPoints-points {
			return errors.New("quality points exceed the v1 limit")
		}
		points += item.MaximumPoints
	}
	return nil
}

func validateCatalogExclusions(exclusions []CatalogExclusion) error {
	if exclusions == nil {
		return errors.New("exclusions must be a canonical array")
	}
	if len(exclusions) > maxCatalogExclusions {
		return fmt.Errorf("exclusions contain more than %d entries", maxCatalogExclusions)
	}
	for index, exclusion := range exclusions {
		if index > 0 && exclusions[index-1].Code >= exclusion.Code {
			return errors.New("exclusions are not strictly sorted by code")
		}
		if !validID(exclusion.Code) || !validCatalogUnbiasedText(exclusion.Code, 128) {
			return fmt.Errorf("exclusion %d code is invalid", index)
		}
		if !validCatalogUnbiasedText(exclusion.Condition, 2_000) {
			return fmt.Errorf("exclusion %d condition is invalid", index)
		}
	}
	return nil
}

func validCatalogUnbiasedText(value string, maximum int) bool {
	if !validEvaluationText(value, maximum) {
		return false
	}
	words := strings.FieldsFunc(strings.ToLower(value), func(character rune) bool {
		return (character < 'a' || character > 'z') && (character < '0' || character > '9')
	})
	for index, word := range words {
		switch word {
		case "baseline", "candidate", "treatment", "arm", "order", "token", "tokens",
			"tool", "tools", "toolcall", "toolcalls", "mcp", "navigator", "scopesifter":
			return false
		}
		if index > 0 && words[index-1] == "scope" && word == "sifter" {
			return false
		}
	}
	return true
}

func findCatalogRepository(language CatalogLanguage, taskSlug string) (catalogRepositoryIdentity, bool) {
	index := sort.Search(len(lockedCatalogRepositories), func(index int) bool {
		candidate := lockedCatalogRepositories[index]
		return repositoryCountKey(candidate.language, candidate.taskSlug) >= repositoryCountKey(language, taskSlug)
	})
	if index == len(lockedCatalogRepositories) {
		return catalogRepositoryIdentity{}, false
	}
	identity := lockedCatalogRepositories[index]
	return identity, identity.language == language && identity.taskSlug == taskSlug
}

func repositoryCountKey(language CatalogLanguage, taskSlug string) string {
	return string(language) + "." + taskSlug
}

func containsCatalogFamily(value CatalogTaskFamily) bool {
	switch value {
	case CatalogFamilyCode, CatalogFamilyReview, CatalogFamilyExplain:
		return true
	default:
		return false
	}
}

func containsCatalogTier(value CatalogTaskTier) bool {
	switch value {
	case CatalogTierSmall, CatalogTierMedium, CatalogTierLarge, CatalogTierHuge:
		return true
	default:
		return false
	}
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

<?php
// Parses the queries of search-cases.json the way FreshRSS reads a filter and
// reports, for each, the tree it built and which of the entries it matches.
// Bounds of dates relative to the current time are left out ("volatile").
// Usage: php search.php <user> < search-cases.json
declare(strict_types=1);
require '/var/www/FreshRSS/cli/_cli.php';

cliInitUser($argv[1]);
$in = json_decode(stream_get_contents(STDIN), true, flags: JSON_THROW_ON_ERROR);
FreshRSS_Context::userConf()->queries = $in['queries'];

// An entry finds its category through its feed.
$pdo = new PDO('sqlite:' . DATA_PATH . '/users/' . $argv[1] . '/db.sqlite');
$pdo->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
$pdo->exec("INSERT OR IGNORE INTO category (id, name, attributes) VALUES (2, 'Second', '[]')");
foreach ($in['feeds'] as $feed) {
	$pdo->prepare("INSERT INTO feed (id, url, category, name, attributes) VALUES (?, ?, ?, ?, '[]')")
		->execute([$feed['id'], 'http://feeds.freshgo.test/' . $feed['id'], $feed['category'], 'Feed ' . $feed['id']]);
}

// Text is stored HTML-encoded, content is HTML already.
$stored = static fn(string $s): string => htmlspecialchars($s, ENT_COMPAT, 'UTF-8');
$entries = [];
foreach ($in['entries'] as $e) {
	$entry = new FreshRSS_Entry($e['feed'], 'guid-' . $e['id'], $stored($e['title']), '', $e['content'], $stored($e['link']), $e['published']);
	$entry->_authors(array_map($stored, $e['authors']));
	$entry->_tags(array_map($stored, $e['tags']));
	$entry->_id($e['id']);
	$entry->_lastModified($e['modified']);
	$entry->_lastUserModified($e['userModified']);
	$entries[] = $entry;
}

const TEXTS = ['Intitle' => 'intitle', 'Intext' => 'intext', 'Author' => 'author', 'Inurl' => 'inurl', 'Tags' => 'tags', 'Search' => 'search'];
const DATES = ['Date' => 'date', 'Pubdate' => 'pubdate', 'ModifiedDate' => 'mdate', 'Userdate' => 'userdate'];

function tree(FreshRSS_BooleanSearch|FreshRSS_Search $s, bool $volatile): array|stdClass {
	if ($s instanceof FreshRSS_BooleanSearch) {
		return ['op' => $s->operator(), 'searches' => array_map(static fn($sub) => tree($sub, $volatile), $s->searches())];
	}
	$out = [];
	$put = static function (string $key, mixed $value) use (&$out): void {
		if ($value !== null && $value !== []) {
			$out[$key] = $value;
		}
	};
	foreach (['', 'Not'] as $not) {
		$prefix = $not === '' ? '' : '-';
		$put($prefix . 'e', $s->{"get{$not}EntryIds"}());
		$put($prefix . 'f', $s->{"get{$not}FeedIds"}());
		$put($prefix . 'c', $s->{"get{$not}CategoryIds"}());
		$put($prefix . 'L', $s->{"get{$not}LabelIds"}());
		$put($prefix . 'labels', $s->{"get{$not}LabelNames"}(true));
		foreach (TEXTS as $getter => $key) {
			$put($prefix . $key, $s->{"get{$not}{$getter}"}(true));
			$put($prefix . $key . '~', $s->{"get{$not}{$getter}Regex"}());
		}
		if (!$volatile) {
			foreach (DATES as $getter => $key) {
				$put($prefix . $key . '>', $s->{"get{$not}Min{$getter}"}());
				$put($prefix . $key . '<', $s->{"get{$not}Max{$getter}"}());
			}
		}
	}
	return $out === [] ? new stdClass() : $out;
}

$lines = [];
foreach ($in['cases'] as $case) {
	$result = ['q' => $case['q']];
	try {
		$search = new FreshRSS_BooleanSearch($case['q']);
		$result['tree'] = tree($search, !empty($case['volatile']));
		$result['matches'] = [];
		foreach ($entries as $i => $entry) {
			if ($entry->matches($search)) {
				$result['matches'][] = $i;
			}
		}
	} catch (Minz_BadRequestException $ex) {
		$result['error'] = $ex->getMessage();
	}
	$lines[] = "\t" . json_encode($result, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR);
}
echo "[\n", implode(",\n", $lines), "\n]\n";

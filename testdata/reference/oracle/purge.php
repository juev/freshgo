<?php
// Fills the database of a FreshRSS user with the feeds of purge-cases.json
// ("fill") and reports how many entries of every group cli/purge.php left
// ("kept"). Entries of a group share everything the cleanup looks at; "age"
// is how long ago, in seconds, the feed last listed them.
// Usage: php purge.php fill|kept <user>
declare(strict_types=1);
require '/var/www/FreshRSS/constants.php';

[, $mode, $user] = $argv;
$cases = json_decode(file_get_contents(__DIR__ . '/purge-cases.json'), true, flags: JSON_THROW_ON_ERROR);
$pdo = new PDO('sqlite:' . DATA_PATH . '/users/' . $user . '/db.sqlite');
$pdo->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);

if ($mode === 'kept') {
	$kept = [];
	$count = $pdo->prepare('SELECT COUNT(*) FROM entry e INNER JOIN feed f ON e.id_feed = f.id WHERE f.name = ? AND e.guid LIKE ?');
	foreach ($cases['feeds'] as $feed) {
		foreach ($feed['groups'] as $g => $group) {
			$count->execute([$feed['name'], "$g-%"]);
			$kept[$feed['name']][] = (int)$count->fetchColumn();
		}
	}
	echo json_encode($kept, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES), "\n";
	exit(0);
}

$attributes = static fn(array $of): string => array_key_exists('archiving', $of) ? json_encode(['archiving' => $of['archiving']]) : '[]';
$categories = ['' => 1];
foreach ($cases['categories'] as $category) {
	$pdo->prepare('INSERT INTO category (name, attributes) VALUES (?, ?)')->execute([$category['name'], $attributes($category)]);
	$categories[$category['name']] = (int)$pdo->lastInsertId();
}
$pdo->exec("INSERT INTO tag (name, attributes) VALUES ('kept', '[]')");
$tag = (int)$pdo->lastInsertId();

$now = time();
$id = $now * 1000000;
$addEntry = $pdo->prepare('INSERT INTO entry (id, guid, title, author, content, link, date, `lastSeen`, hash, is_read, is_favorite, id_feed, tags, attributes)'
	. " VALUES (?, ?, ?, '', '', ?, ?, ?, X'00', ?, ?, ?, '', '[]')");
$addLabel = $pdo->prepare('INSERT INTO entrytag (id_tag, id_entry) VALUES (?, ?)');
$pdo->beginTransaction();
foreach ($cases['feeds'] as $feed) {
	$pdo->prepare('INSERT INTO feed (url, category, name, `lastUpdate`, attributes) VALUES (?, ?, ?, ?, ?)')->execute([
		'http://feeds.freshgo.test/' . rawurlencode($feed['name']), $categories[$feed['category'] ?? ''], $feed['name'], $now, $attributes($feed),
	]);
	$feedId = (int)$pdo->lastInsertId();
	foreach ($feed['groups'] as $g => $group) {
		for ($i = 0; $i < $group['count']; $i++) {
			$id++;
			$seen = $now - $group['age'];
			$addEntry->execute(["$id", "$g-$i", "Entry $g-$i", "http://feeds.freshgo.test/$feedId/$g-$i", $seen, $seen,
				empty($group['read']) ? 0 : 1, empty($group['favorite']) ? 0 : 1, $feedId]);
			if (!empty($group['labelled'])) {
				$addLabel->execute([$tag, "$id"]);
			}
		}
	}
}
$pdo->commit();

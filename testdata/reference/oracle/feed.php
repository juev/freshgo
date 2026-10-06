<?php
// Parses the feeds given as arguments the way a FreshRSS refresh does and
// prints what would be stored for each of them.
require '/var/www/FreshRSS/constants.php';
require LIB_PATH . '/lib_rss.php';
FreshRSS_Context::initSystem();

$out = [];
foreach (array_slice($argv, 1) as $path) {
	$simplePie = new FreshRSS_SimplePieCustom();
	$simplePie->enable_cache(false);
	$simplePie->set_raw_data(file_get_contents($path));
	$simplePie->init();

	$feed = new FreshRSS_Feed('http://feeds.freshgo.test/' . basename($path));
	$guids = $feed->loadGuids($simplePie);
	// FreshRSS stores text HTML-encoded and replaces a missing date with the
	// current time; both are undone here to keep the output stable and plain.
	$plain = static fn(string $s): string => htmlspecialchars_decode($s, ENT_QUOTES);
	$dates = array_map(static fn($item) => (int)$item->get_date('U'), array_reverse($simplePie->get_items()));
	$entries = [];
	foreach ($feed->loadEntries($simplePie) as $i => $entry) {
		$entries[] = [
			'guid' => $entry->guid(),
			'title' => $plain($entry->title()),
			'authors' => array_values(array_map($plain, $entry->authors(false))),
			'content' => $entry->content(false),
			'link' => $plain($entry->link(true)),
			'date' => $dates[$i],
			'tags' => array_values(array_map($plain, $entry->tags(false))),
			'attributes' => $entry->attributes(),
		];
	}
	$out[basename($path)] = [
		'error' => $simplePie->error(),
		'title' => $simplePie->get_title(),
		'link' => $simplePie->get_link(),
		'description' => $simplePie->get_description(),
		'self' => $simplePie->get_links('self'),
		'hub' => $simplePie->get_links('hub'),
		'unicityCriteria' => $feed->attributeString('unicityCriteria'),
		'inError' => $feed->inError(),
		'guids' => $guids,
		'entries' => $entries,
	];
}
echo json_encode($out, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT | JSON_THROW_ON_ERROR), "\n";

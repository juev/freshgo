<?php
// Scrapes the pages of scrape-cases.json, served by a local web server, the
// way a FreshRSS refresh does and prints what would be stored.
require '/var/www/FreshRSS/constants.php';
require LIB_PATH . '/lib_rss.php';
FreshRSS_Context::initSystem();
// The RSS view asks for a translation of the channel description; nothing reads it.
if (!function_exists('_t')) {
	function _t(string $key, mixed ...$args): string {
		return $key;
	}
}
// Every case must read its page, not what an earlier case left in the cache.
FreshRSS_Context::systemConf()->limits = array_merge(FreshRSS_Context::systemConf()->limits, ['cache_duration' => -1]);

$plain = static fn(string $s): string => htmlspecialchars_decode($s, ENT_QUOTES);
$out = [];
foreach (json_decode(stream_get_contents(STDIN), true, flags: JSON_THROW_ON_ERROR) as $case) {
	$feed = new FreshRSS_Feed('http://127.0.0.1:8080/' . $case['page'], false);
	$feed->_kind($case['kind']);
	$feed->_name($case['feedName']);
	$feed->_attributes($case['attributes']);
	$simplePie = in_array($case['kind'], [FreshRSS_Feed::KIND_HTML_XPATH, FreshRSS_Feed::KIND_XML_XPATH], true)
		? $feed->loadHtmlXpath() : $feed->loadJson();
	if ($simplePie === null) {
		$out[$case['name']] = ['failed' => true, 'entries' => []];
		continue;
	}
	$feed->loadGuids($simplePie);
	// A missing date becomes the current time in FreshRSS: report it as 0.
	$dates = array_map(static fn($item) => $item->get_item_tags('', 'pubDate')[0]['data'] ?? '', array_reverse($simplePie->get_items()));
	$started = time();
	$entries = [];
	foreach ($feed->loadEntries($simplePie) as $i => $entry) {
		$date = (int)$entry->date(true);
		$entries[] = [
			'guid' => $entry->guid(),
			'title' => $plain($entry->title()),
			'authors' => array_values(array_map($plain, $entry->authors(false))),
			'content' => $entry->content(false),
			'link' => $plain($entry->link(true)),
			'date' => $date >= $started - 5 ? 0 : $date,
			'tags' => array_values(array_map($plain, $entry->tags(false))),
			'attributes' => $entry->attributes(),
		];
	}
	$out[$case['name']] = ['failed' => false, 'title' => $plain($simplePie->get_title() ?? ''), 'entries' => $entries];
}
echo json_encode($out, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT | JSON_THROW_ON_ERROR), "\n";

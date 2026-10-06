<?php
// Gets the full text of the articles of fulltext-cases.json, served by a
// local web server, the way a FreshRSS refresh does and prints it.
require '/var/www/FreshRSS/constants.php';
require LIB_PATH . '/lib_rss.php';
FreshRSS_Context::initSystem();
// Every case must read its page, not what an earlier case left in the cache.
FreshRSS_Context::systemConf()->limits = array_merge(FreshRSS_Context::systemConf()->limits, ['cache_duration' => -1]);

$out = [];
foreach (json_decode(stream_get_contents(STDIN), true, flags: JSON_THROW_ON_ERROR) as $case) {
	$feed = new FreshRSS_Feed('http://127.0.0.1:8080/feed.xml', false);
	$feed->_pathEntries(htmlspecialchars($case['selector'], ENT_COMPAT, 'UTF-8'));
	if (isset($case['filter'])) {
		$feed->_attribute('path_entries_filter', $case['filter']);
	}
	$entry = new FreshRSS_Entry(0, 'guid', 'Title', '', 'summary', 'http://127.0.0.1:8080/' . $case['page']);
	$entry->_feed($feed);
	$out[$case['name']] = $entry->getContentByParsing();
}
echo json_encode($out, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT | JSON_THROW_ON_ERROR), "\n";

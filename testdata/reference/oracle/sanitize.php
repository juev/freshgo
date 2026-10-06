<?php
// Runs the FreshRSS HTML sanitizer over the cases given on standard input.
require '/var/www/FreshRSS/constants.php';
require LIB_PATH . '/lib_rss.php';
FreshRSS_Context::initSystem();

$cases = json_decode(stream_get_contents(STDIN), true, flags: JSON_THROW_ON_ERROR);
foreach ($cases as &$case) {
	$case['out'] = FreshRSS_SimplePieCustom::sanitizeHTML($case['html'], $case['base']);
}
echo json_encode($cases, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT | JSON_THROW_ON_ERROR), "\n";

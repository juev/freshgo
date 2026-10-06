<?php
// Gives a feed a custom icon the way the FreshRSS web interface does.
// Usage: php set-custom-icon.php <user> <feed id> <image file>
declare(strict_types=1);
require('/var/www/FreshRSS/cli/_cli.php');

[, $user, $feedId, $file] = $argv;
cliInitUser($user);
$feed = FreshRSS_Factory::createFeedDao()->searchById((int)$feedId);
if ($feed === null) {
	fail("No feed $feedId for $user");
}
$contents = file_get_contents($file);
if ($contents === false) {
	fail("Cannot read $file");
}
$values = [];
$feed->setCustomFavicon($contents, '', $values);
echo 'Custom icon: ', $feed->hashFavicon(skipCache: true), "\n";

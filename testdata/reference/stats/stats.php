<?php
// Reports the statistics FreshRSS shows a user: FreshRSS_StatsDAO over the
// data directory of the reference installation.
// Usage: php stats.php <user> <feed id>
declare(strict_types=1);
require '/var/www/FreshRSS/cli/_cli.php';

cliInitUser($argv[1]);
$feed = (int)$argv[2];
$dao = FreshRSS_Factory::createStatsDAO();

$unread = [];
foreach (['id', 'date'] as $field) {
	foreach (['day', 'month', 'year'] as $granularity) {
		$unread[$field . '/' . $granularity] = $dao->getMaxUnreadDates($field, $granularity, 100, FreshRSS_Feed::PRIORITY_HIDDEN);
	}
}
$unread['date/day/main'] = $dao->getMaxUnreadDates('date', 'day', 3, FreshRSS_Feed::PRIORITY_MAIN_STREAM);

$repartition = static fn(?int $id): array => [
	'totals' => $dao->calculateEntryRepartitionPerFeed($id),
	'hour' => $dao->calculateEntryRepartitionPerFeedPerHour($id),
	'weekday' => $dao->calculateEntryRepartitionPerFeedPerDayOfWeek($id),
	'month' => $dao->calculateEntryRepartitionPerFeedPerMonth($id),
	'hourly' => $dao->calculateEntryAveragePerFeedPerHour($id),
	'daily' => $dao->calculateEntryAveragePerFeedPerDayOfWeek($id),
	'monthly' => $dao->calculateEntryAveragePerFeedPerMonth($id),
];

echo json_encode([
	'totals' => $dao->calculateEntryRepartition(),
	'feedsByCategory' => $dao->calculateFeedByCategory(),
	'entriesByCategory' => $dao->calculateEntryByCategory(),
	'topFeeds' => $dao->calculateTopFeed(),
	'all' => $repartition(null),
	'feed' => $repartition($feed),
	'unread' => $unread,
], JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE), "\n";

<?php
// Builds, for every sharing service of app/shares.php and every case of
// share-cases.json, the address FreshRSS would open: FreshRSS_Share::url with
// the transforms of the service, done here by hand because the class needs
// an installation to be loaded. Also lists what FreshRSS says of each
// service: its name, whether it needs an address of its own, its HTML tag.
// Usage: php share.php < share-cases.json
declare(strict_types=1);

$shares = include '/var/www/FreshRSS/app/shares.php';
$names = (include '/var/www/FreshRSS/app/i18n/en/gen.php')['share'];
$cases = json_decode(stream_get_contents(STDIN), true, flags: JSON_THROW_ON_ERROR);

$out = [];
foreach ($shares as $type => $share) {
	$transform = static function (string $data) use ($share): string {
		foreach ($share['transform'] as $action) {
			$data = call_user_func($action, $data);
		}
		return $data;
	};
	$urls = [];
	foreach ($cases as $case) {
		$urls[] = str_replace(
			['~ID~', '~URL~', '~TITLE~', '~LINK~'],
			[$transform($case['id']), $case['base'], $transform($case['title']), $transform($case['link'])],
			$share['url']
		);
	}
	$out[] = [
		'type' => $type,
		'name' => $names[$type] ?? $type,
		'advanced' => ($share['form'] ?? 'simple') === 'advanced',
		'button' => ($share['HTMLtag'] ?? '') === 'button',
		'help' => $share['help'] ?? '',
		'urls' => $urls,
	];
}
echo json_encode($out, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE), "\n";

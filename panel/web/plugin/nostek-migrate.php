<?php
/**
 * Plugin Name: Nostek Migrate
 * Description: Lets Nostek Panel copy this site (files and database) to a new server. Read only; remove it after the move.
 * Version: 1.0.0
 * Requires at least: 5.6
 * Requires PHP: 7.4
 * Author: Nostek Panel
 * License: MIT
 * Text Domain: nostek-migrate
 */

if (!defined('ABSPATH')) {
	exit;
}

final class Nostek_Migrate {
	const VERSION   = '1.0.0';
	const OPT       = 'nostek_migrate_key';     // sha256 of the key, expiry, failures
	const NS        = 'nostek-migrate/v1';
	const TTL       = 86400;                    // a code lives one day
	const MAX_FAILS = 10;                       // wrong keys per hour before every call is refused
	const LIST_MAX  = 5000;                     // entries per files listing
	const BATCH_MAX = 1000;                     // files per batch
	const BATCH_BYTES = 67108864;               // 64 MB per batch; larger files go by range
	const RANGE_MAX = 33554432;                 // 32 MB per range
	const ROWS_MAX  = 2000;
	const SQL_BYTES = 8388608;                  // 8 MB of SQL per table call

	public static function boot() {
		register_activation_hook(__FILE__, array(__CLASS__, 'activate'));
		register_deactivation_hook(__FILE__, array(__CLASS__, 'deactivate'));
		add_action('rest_api_init', array(__CLASS__, 'routes'));
		add_action('admin_menu', array(__CLASS__, 'menu'));
	}

	public static function activate() {
		self::new_key();
	}

	public static function deactivate() {
		delete_option(self::OPT);
		delete_transient('nostek_migrate_code');
	}

	/** new_key makes a random key, stores only its hash and keeps the code for the admin page for one view. */
	public static function new_key() {
		$key = rtrim(strtr(base64_encode(random_bytes(32)), '+/', '-_'), '=');
		update_option(self::OPT, array('hash' => hash('sha256', $key), 'exp' => time() + self::TTL, 'fails' => array()), false);
		$code = 'nm1.' . rtrim(strtr(base64_encode(wp_json_encode(array('u' => home_url('/'), 'k' => $key))), '+/', '-_'), '=');
		set_transient('nostek_migrate_code', $code, 600);
		return $code;
	}

	public static function menu() {
		add_management_page('Nostek Migrate', 'Nostek Migrate', 'manage_options', 'nostek-migrate', array(__CLASS__, 'page'));
	}

	public static function page() {
		if (!current_user_can('manage_options')) {
			return;
		}
		$https = 0 === strpos(home_url('/'), 'https://');
		if ($https && isset($_POST['nostek_new']) && check_admin_referer('nostek_migrate_new')) {
			self::new_key();
		}
		$opt  = get_option(self::OPT);
		$code = get_transient('nostek_migrate_code');
		echo '<div class="wrap"><h1>Nostek Migrate</h1>';
		if (!$https) {
			echo '<p><strong>This site does not use HTTPS.</strong> The migration code travels to the new server, so this site needs HTTPS first.</p></div>';
			return;
		}
		if (!is_array($opt)) {
			echo '<p>The migration is finished and the code is gone. You can deactivate and delete this plugin.</p>';
		} elseif ($code) {
			delete_transient('nostek_migrate_code');
			echo '<p>Paste this code in Nostek Panel → the new website → <em>Migrate</em>. It is shown only once and works for 24 hours.</p>';
			echo '<p><textarea readonly rows="4" style="width:100%;font-family:monospace" onclick="this.select()">' . esc_textarea($code) . '</textarea></p>';
		} else {
			echo '<p>A code was made on ' . esc_html(wp_date('Y-m-d H:i', (int) $opt['exp'] - self::TTL)) . ' and works until ' . esc_html(wp_date('Y-m-d H:i', (int) $opt['exp'])) . '. It is shown only once; make a new one if you did not copy it.</p>';
		}
		echo '<form method="post">';
		wp_nonce_field('nostek_migrate_new');
		echo '<p><button class="button button-primary" name="nostek_new" value="1">New code</button></p></form>';
		echo '<p>The plugin only reads this site. Deactivate and delete it when the move is done.</p></div>';
	}

	public static function routes() {
		$auth = array(__CLASS__, 'auth');
		register_rest_route(self::NS, '/info', array('methods' => 'GET', 'callback' => array(__CLASS__, 'info'), 'permission_callback' => $auth));
		register_rest_route(self::NS, '/files', array(
			array('methods' => 'GET', 'callback' => array(__CLASS__, 'files'), 'permission_callback' => $auth),
			array('methods' => 'POST', 'callback' => array(__CLASS__, 'batch'), 'permission_callback' => $auth),
		));
		register_rest_route(self::NS, '/file', array('methods' => 'GET', 'callback' => array(__CLASS__, 'range'), 'permission_callback' => $auth));
		register_rest_route(self::NS, '/table', array('methods' => 'GET', 'callback' => array(__CLASS__, 'table'), 'permission_callback' => $auth));
		register_rest_route(self::NS, '/finish', array('methods' => 'POST', 'callback' => array(__CLASS__, 'finish'), 'permission_callback' => $auth));
	}

	/** auth checks X-Nostek-Key against the stored hash; too many wrong keys lock every call until a new code. */
	public static function auth($req) {
		$opt = get_option(self::OPT);
		if (!is_array($opt) || empty($opt['hash']) || time() > (int) $opt['exp']) {
			return new WP_Error('nostek_expired', 'no valid migration code: make a new one on the old site', array('status' => 403));
		}
		$now   = time();
		$fails = array_values(array_filter((array) $opt['fails'], function ($t) use ($now) { return $t > $now - 3600; }));
		if (count($fails) >= self::MAX_FAILS) {
			return new WP_Error('nostek_locked', 'too many wrong codes: make a new one on the old site', array('status' => 403));
		}
		$key = (string) $req->get_header('x-nostek-key');
		if ('' !== $key && hash_equals($opt['hash'], hash('sha256', $key))) {
			return true;
		}
		$fails[]      = $now;
		$opt['fails'] = $fails;
		update_option(self::OPT, $opt, false);
		return new WP_Error('nostek_key', 'wrong migration code', array('status' => 403));
	}

	public static function info() {
		global $wpdb, $wp_version, $table_prefix;
		$tables = array();
		foreach ((array) $wpdb->get_results('SHOW TABLE STATUS', ARRAY_A) as $t) {
			if (null === $t['Engine'] && null === $t['Rows']) {
				continue; // a view
			}
			$tables[] = array('name' => $t['Name'], 'rows' => (int) $t['Rows'], 'bytes' => (int) $t['Data_length'] + (int) $t['Index_length']);
		}
		return array(
			'plugin'    => self::VERSION,
			'wordpress' => $wp_version,
			'php'       => PHP_VERSION,
			'home'      => home_url(),
			'siteurl'   => site_url(),
			'prefix'    => $table_prefix,
			'multisite' => is_multisite(),
			'charset'   => $wpdb->charset,
			'tables'    => $tables,
		);
	}

	private static function root() {
		return rtrim(str_replace('\\', '/', ABSPATH), '/');
	}

	/** rel checks a relative path from the panel: no absolute names, no .., no backslash, no NUL. */
	private static function rel($p) {
		$p = (string) $p;
		if ('' === $p || '/' === $p[0] || false !== strpos($p, '\\') || false !== strpos($p, "\0")) {
			return null;
		}
		foreach (explode('/', $p) as $part) {
			if ('' === $part || '.' === $part || '..' === $part) {
				return null;
			}
		}
		return $p;
	}

	/** cmp orders paths the way walk visits them: a folder's entries right after it (the separator sorts first). */
	private static function cmp($a, $b) {
		return strcmp(str_replace('/', "\0", $a), str_replace('/', "\0", $b));
	}

	/** walk lists entries under ABSPATH in visiting order (see cmp), after $after, without following links. */
	private static function walk($dir, $rel, $after, &$out, $max) {
		$names = @scandir($dir);
		if (false === $names) {
			return;
		}
		sort($names, SORT_STRING);
		foreach ($names as $n) {
			if ('.' === $n || '..' === $n) {
				continue;
			}
			if (count($out) >= $max) {
				return;
			}
			$p    = '' === $rel ? $n : $rel . '/' . $n;
			$full = $dir . '/' . $n;
			$link = is_link($full);
			$isdir = !$link && is_dir($full);
			// skip whole folders that sort before the cursor and do not contain it
			if (self::cmp($p, $after) <= 0 && !($isdir && 0 === strpos($after, $p . '/'))) {
				continue;
			}
			$st = @lstat($full);
			if (false === $st) {
				continue;
			}
			if (self::cmp($p, $after) > 0) {
				$e = array('path' => $p, 'type' => $link ? 'link' : ($isdir ? 'dir' : 'file'), 'mode' => $st['mode'] & 0777, 'mtime' => $st['mtime']);
				if ($link) {
					$e['target'] = (string) @readlink($full);
				} elseif (!$isdir) {
					$e['size'] = $st['size'];
				}
				$out[] = $e;
			}
			if ($isdir) {
				self::walk($full, $p, $after, $out, $max);
			}
		}
	}

	public static function files($req) {
		$after = (string) $req->get_param('after');
		if ('' !== $after && null === self::rel($after)) {
			return new WP_Error('nostek_path', 'bad cursor', array('status' => 400));
		}
		$out = array();
		self::walk(self::root(), '', $after, $out, self::LIST_MAX);
		return array('entries' => $out, 'more' => count($out) >= self::LIST_MAX);
	}

	/** tar_header writes one ustar header; long names go into a GNU long-name entry. */
	private static function tar_header($name, $size, $mode, $mtime, $type, $link = '') {
		$out = '';
		if (strlen($name) > 100) {
			$out .= self::tar_header('././@LongLink', strlen($name) + 1, 0, 0, 'L');
			$out .= str_pad($name . "\0", (int) ceil((strlen($name) + 1) / 512) * 512, "\0");
			$name = substr($name, 0, 100);
		}
		$h = str_pad($name, 100, "\0")
			. sprintf('%07o', $mode & 0777) . "\0"
			. sprintf('%07o', 0) . "\0" . sprintf('%07o', 0) . "\0"
			. sprintf('%011o', $size) . "\0"
			. sprintf('%011o', max(0, (int) $mtime)) . "\0"
			. '        ' . $type
			. str_pad(substr($link, 0, 100), 100, "\0")
			. "ustar\0" . '00'
			. str_repeat("\0", 32) . str_repeat("\0", 32)
			. str_repeat("\0", 8) . str_repeat("\0", 8)
			. str_repeat("\0", 155) . str_repeat("\0", 12);
		$sum = 0;
		for ($i = 0; $i < 512; $i++) {
			$sum += ord($h[$i]);
		}
		$h = substr_replace($h, sprintf('%06o', $sum) . "\0 ", 148, 8);
		return $out . $h;
	}

	/** batch streams the asked files as one tar; it ends after the files, a size limit or a missing file is skipped. */
	public static function batch($req) {
		$paths = $req->get_json_params();
		$paths = is_array($paths) && isset($paths['paths']) && is_array($paths['paths']) ? $paths['paths'] : null;
		if (null === $paths || count($paths) > self::BATCH_MAX) {
			return new WP_Error('nostek_batch', 'send {"paths": [...]} with at most ' . self::BATCH_MAX . ' paths', array('status' => 400));
		}
		foreach ($paths as $p) {
			if (!is_string($p) || null === self::rel($p)) {
				return new WP_Error('nostek_path', 'bad path', array('status' => 400));
			}
		}
		$root = self::root();
		while (ob_get_level()) {
			ob_end_clean();
		}
		@set_time_limit(120);
		header('Content-Type: application/x-tar');
		header('X-Content-Type-Options: nosniff');
		$sent = 0;
		foreach ($paths as $p) {
			$full = $root . '/' . $p;
			if (is_link($full)) {
				echo self::tar_header($p, 0, 0777, @lstat($full)['mtime'] ?? 0, '2', (string) @readlink($full));
				continue;
			}
			if (!is_file($full)) {
				continue;
			}
			$size = filesize($full);
			if ($sent + $size > self::BATCH_BYTES && $sent > 0) {
				break; // the panel asks again for the rest
			}
			$fh = @fopen($full, 'rb');
			if (false === $fh) {
				continue;
			}
			echo self::tar_header($p, $size, fileperms($full), filemtime($full), '0');
			$left = $size;
			while ($left > 0 && !feof($fh)) {
				$chunk = fread($fh, (int) min(1048576, $left));
				if (false === $chunk || '' === $chunk) {
					break;
				}
				echo $chunk;
				$left -= strlen($chunk);
			}
			fclose($fh);
			if ($left > 0) {
				echo str_repeat("\0", $left); // the file shrank while being read: the panel sees the size mismatch later
			}
			$pad = (512 - $size % 512) % 512;
			echo str_repeat("\0", $pad);
			$sent += $size;
			flush();
		}
		echo str_repeat("\0", 1024);
		exit;
	}

	public static function range($req) {
		$p = self::rel($req->get_param('path'));
		$off = (int) $req->get_param('offset');
		$len = (int) $req->get_param('length');
		if (null === $p || $off < 0 || $len <= 0 || $len > self::RANGE_MAX) {
			return new WP_Error('nostek_range', 'bad path or range', array('status' => 400));
		}
		$full = self::root() . '/' . $p;
		if (is_link($full) || !is_file($full)) {
			return new WP_Error('nostek_missing', 'no such file', array('status' => 404));
		}
		$fh = @fopen($full, 'rb');
		if (false === $fh || 0 !== fseek($fh, $off)) {
			return new WP_Error('nostek_read', 'cannot read the file', array('status' => 500));
		}
		while (ob_get_level()) {
			ob_end_clean();
		}
		header('Content-Type: application/octet-stream');
		while ($len > 0 && !feof($fh)) {
			$chunk = fread($fh, (int) min(1048576, $len));
			if (false === $chunk || '' === $chunk) {
				break;
			}
			echo $chunk;
			$len -= strlen($chunk);
		}
		fclose($fh);
		exit;
	}

	/** quote returns a SQL literal: NULL, a number as written, binary as hex, text escaped by the connection. */
	private static function quote($v, $binary) {
		global $wpdb;
		if (null === $v) {
			return 'NULL';
		}
		if ($binary) {
			return '' === $v ? "''" : '0x' . bin2hex($v);
		}
		// _real_escape hides % behind a placeholder for prepare(); take it out again, the value is used as is
		return "'" . $wpdb->remove_placeholder_escape($wpdb->_real_escape($v)) . "'";
	}

	/** table answers the SQL of one table in pieces: CREATE TABLE first, then INSERT batches after a key. */
	public static function table($req) {
		global $wpdb;
		$name  = (string) $req->get_param('name');
		$after = $req->get_param('after');
		$limit = min(self::ROWS_MAX, max(1, (int) ($req->get_param('limit') ?: self::ROWS_MAX)));
		$known = array();
		foreach ((array) $wpdb->get_col('SHOW TABLES') as $t) {
			$known[$t] = true;
		}
		if (!isset($known[$name])) {
			return new WP_Error('nostek_table', 'no such table', array('status' => 404));
		}
		$q    = '`' . str_replace('`', '``', $name) . '`';
		$cols = $wpdb->get_results("SHOW COLUMNS FROM $q", ARRAY_A);
		$pk   = array();
		$bin  = array();
		foreach ($cols as $c) {
			if ('PRI' === $c['Key']) {
				$pk[] = $c['Field'];
			}
			$bin[$c['Field']] = (bool) preg_match('/blob|binary|bit|geometry|point|linestring|polygon/i', $c['Type']);
		}
		$sql = '';
		if (null === $after || '' === $after) {
			$create = $wpdb->get_row("SHOW CREATE TABLE $q", ARRAY_N);
			$sql   .= "DROP TABLE IF EXISTS $q;\n" . $create[1] . ";\n";
		}
		$keyset = 1 === count($pk);
		if ($keyset) {
			$k     = '`' . str_replace('`', '``', $pk[0]) . '`';
			$where = (null === $after || '' === $after) ? '' : $wpdb->prepare(" WHERE $k > %s", (string) $after);
			$rows  = $wpdb->get_results("SELECT * FROM $q$where ORDER BY $k LIMIT $limit", ARRAY_A);
		} else {
			$off  = (null === $after || '' === $after) ? 0 : (int) $after;
			$rows = $wpdb->get_results("SELECT * FROM $q LIMIT $limit OFFSET $off", ARRAY_A);
		}
		$next = null;
		$line = '';
		$n    = 0;
		foreach ((array) $rows as $r) {
			$vals = array();
			foreach ($r as $f => $v) {
				$vals[] = self::quote($v, !empty($bin[$f]));
			}
			$line .= ('' === $line ? "INSERT INTO $q VALUES\n(" : ",\n(") . implode(',', $vals) . ')';
			$n++;
			$next = $keyset ? $r[$pk[0]] : ((int) ($after ?: 0) + $n);
			if (strlen($line) > self::SQL_BYTES) {
				break;
			}
		}
		if ('' !== $line) {
			$sql .= $line . ";\n";
		}
		$more = count((array) $rows) >= $limit || $n < count((array) $rows);
		return array('sql' => $sql, 'rows' => $n, 'next' => $more ? (string) $next : null);
	}

	public static function finish() {
		delete_option(self::OPT);
		delete_transient('nostek_migrate_code');
		return array('done' => true);
	}
}

Nostek_Migrate::boot();

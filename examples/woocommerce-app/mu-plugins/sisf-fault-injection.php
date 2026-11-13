<?php
/**
 * Plugin Name: SISF trial fault injection
 * Description: Trial only. Reads the option sisf_fault, set with
 * `wp option update sisf_fault '{"error_percent":50}' --format=json`, and
 * injects the faults the experiment must detect: server errors, latency and
 * a checkout that answers 200 but creates no order. Absent option, no effect.
 */
function sisf_fault( $key ) {
	$fault = get_option( 'sisf_fault' );
	return is_array( $fault ) && isset( $fault[ $key ] ) ? (int) $fault[ $key ] : 0;
}

add_action(
	'init',
	function () {
		if ( defined( 'WP_CLI' ) || ( isset( $_SERVER['REQUEST_URI'] ) && '/healthz' === strtok( $_SERVER['REQUEST_URI'], '?' ) ) ) {
			return;
		}
		$latency = sisf_fault( 'latency_ms' );
		if ( $latency > 0 ) {
			usleep( min( $latency, 10000 ) * 1000 );
		}
		$percent = sisf_fault( 'error_percent' );
		if ( $percent > 0 && wp_rand( 1, 100 ) <= $percent ) {
			status_header( 500 );
			exit( 'injected server error' );
		}
	},
	0
);

// A broken checkout: the order is refused after payment validation, with a
// notice, so the page still answers 200.
add_action(
	'woocommerce_after_checkout_validation',
	function ( $data, $errors ) {
		if ( sisf_fault( 'checkout_broken' ) ) {
			$errors->add( 'sisf_fault', 'Checkout is temporarily unavailable.' );
		}
	},
	10,
	2
);

<?php
/**
 * Prints the shop's data as JSON: counts, and a checksum of every order's id,
 * status, total and line items, so data preservation can be compared.
 *
 * Run: wp eval-file seed/counts.php
 */
$orders = wc_get_orders( array( 'limit' => -1, 'orderby' => 'ID', 'order' => 'ASC', 'type' => 'shop_order' ) );
$rows   = array();
foreach ( $orders as $o ) {
	$items = array();
	foreach ( $o->get_items() as $item ) {
		$items[] = $item->get_product_id() . 'x' . $item->get_quantity();
	}
	$rows[] = implode( '|', array( $o->get_id(), $o->get_status(), $o->get_total(), implode( ',', $items ) ) );
}
$zones   = WC_Shipping_Zones::get_zones();
$methods = 0;
foreach ( $zones as $z ) {
	$methods += count( $z['shipping_methods'] );
}
$gateways = array_keys( array_filter( WC()->payment_gateways()->payment_gateways(), function ( $g ) { return 'yes' === $g->enabled; } ) );
echo wp_json_encode(
	array(
		'products'         => (int) ( new WP_Query( array( 'post_type' => 'product', 'post_status' => 'publish', 'posts_per_page' => 1, 'fields' => 'ids' ) ) )->found_posts,
		'customers'        => count( get_users( array( 'role' => 'customer', 'fields' => 'ID' ) ) ),
		'orders'           => count( $orders ),
		'coupons'          => (int) ( new WP_Query( array( 'post_type' => 'shop_coupon', 'post_status' => 'publish', 'posts_per_page' => 1, 'fields' => 'ids' ) ) )->found_posts,
		'shipping_methods' => $methods,
		'gateways'         => $gateways,
		'stock_total'      => (int) $GLOBALS['wpdb']->get_var( "SELECT SUM(CAST(meta_value AS SIGNED)) FROM {$GLOBALS['wpdb']->postmeta} WHERE meta_key = '_stock'" ),
		'orders_sha256'    => hash( 'sha256', implode( "\n", $rows ) ),
		'order_ids'        => array_map( function ( $o ) { return $o->get_id(); }, $orders ),
	)
) . "\n";

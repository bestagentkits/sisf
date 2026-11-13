<?php
/**
 * Plugin Name: SISF sandbox payment
 * Description: A payment gateway that never moves money. It approves every
 * payment with a fake transaction id, except the test token "decline", which
 * it refuses. For the trial and its synthetic purchases only.
 * Version: 1.0.0
 */
add_action(
	'plugins_loaded',
	function () {
		if ( ! class_exists( 'WC_Payment_Gateway' ) ) {
			return;
		}
		class SISF_Sandbox_Gateway extends WC_Payment_Gateway {
			public function __construct() {
				$this->id                 = 'sisf_sandbox';
				$this->method_title       = 'SISF sandbox';
				$this->method_description = 'Approves without moving money. Trial only.';
				$this->title              = 'Sandbox payment (no money moves)';
				$this->has_fields         = false;
				$this->enabled            = 'yes';
				$this->supports           = array( 'products' );
			}

			public function process_payment( $order_id ) {
				$order = wc_get_order( $order_id );
				$token = isset( $_POST['sisf_sandbox_token'] ) ? sanitize_text_field( wp_unslash( $_POST['sisf_sandbox_token'] ) ) : '';
				if ( 'decline' === $token ) {
					wc_add_notice( 'The sandbox declined this payment.', 'error' );
					return array( 'result' => 'failure' );
				}
				$order->payment_complete( 'sandbox-' . wp_generate_uuid4() );
				return array(
					'result'   => 'success',
					'redirect' => $this->get_return_url( $order ),
				);
			}
		}
		add_filter(
			'woocommerce_payment_gateways',
			function ( $gateways ) {
				$gateways[] = 'SISF_Sandbox_Gateway';
				return $gateways;
			}
		);
	}
);

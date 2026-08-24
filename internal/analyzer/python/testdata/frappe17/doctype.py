"""This docstring mentions frappe.get_doc("Fake DocType") and def fake()."""

import frappe
import frappe.model.document as document_module
from frappe import get_all as list_documents
from frappe.model.document import Document


class SalesOrder(Document):
    """A source string mentions frappe.get_all("Fake") but is not code."""

    @frappe.whitelist()
    def load_status(self, name):
        settings = frappe.conf.get("ERP_MODE")
        document = frappe.get_doc("Sales Order", name)
        rows = frappe.get_all("Sales Order", fields=["name"])
        status = frappe.db.get_value("Sales Order", name, "status")
        dynamic = frappe.get_doc(DOCTYPE_NAME)
        return document, rows, status, dynamic

    async def refresh(self, name: str):
        return await frappe.get_doc("Delivery Note", name)


# frappe.get_doc("Comment DocType") and def ignored_comment(): must not count.
literal = "from frappe import ignored_import\nfrappe.get_all('Ignored DocType')"
